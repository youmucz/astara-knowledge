package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const (
	astaraAnswerMaxQueryChars = 2000
	astaraAnswerMaxKBIDs      = 16
	astaraAnswerChannel       = "api"
)

// astaraAnswerRequest is the closed stateless-answer body. Unknown fields are
// rejected at decode; agent, web search, MCP, skills, sandbox, memory,
// session, endpoint, and model authority have no representation here.
type astaraAnswerRequest struct {
	Query            string   `json:"query"`
	Channel          string   `json:"channel"`
	KnowledgeBaseIDs []string `json:"knowledge_base_ids"`
}

// AstaraAnswerHandler serves POST /api/v1/astara/answer: ordinary RAG over
// explicitly scoped knowledge bases with no provider session created or
// recalled. The tenant is derived from the knowledge bases themselves —
// never from client-supplied tenant authority.
type AstaraAnswerHandler struct {
	sessionService       interfaces.SessionService
	knowledgeBaseService interfaces.KnowledgeBaseService
}

func NewAstaraAnswerHandler(
	sessionService interfaces.SessionService,
	knowledgeBaseService interfaces.KnowledgeBaseService,
) *AstaraAnswerHandler {
	return &AstaraAnswerHandler{
		sessionService:       sessionService,
		knowledgeBaseService: knowledgeBaseService,
	}
}

// Answer executes the stateless RAG pipeline and streams the same SSE shape
// the session chat endpoints use (answer / references / error / complete).
func (h *AstaraAnswerHandler) Answer(c *gin.Context) {
	ctx := c.Request.Context()

	var request astaraAnswerRequest
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid answer request"})
		return
	}
	query := strings.TrimSpace(request.Query)
	if query == "" || len([]rune(query)) > astaraAnswerMaxQueryChars {
		c.JSON(http.StatusBadRequest, gin.H{"error": "query is required and bounded"})
		return
	}
	if request.Channel != "" && request.Channel != astaraAnswerChannel {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported channel"})
		return
	}
	if len(request.KnowledgeBaseIDs) == 0 || len(request.KnowledgeBaseIDs) > astaraAnswerMaxKBIDs {
		c.JSON(http.StatusBadRequest, gin.H{"error": "knowledge_base_ids is required and bounded"})
		return
	}

	// Derive the authoritative tenant from the requested knowledge bases:
	// every base must exist and belong to exactly one tenant.
	var tenantID uint64
	seen := make(map[string]bool, len(request.KnowledgeBaseIDs))
	for _, id := range request.KnowledgeBaseIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "duplicate or empty knowledge base id"})
			return
		}
		seen[id] = true
		kb, err := h.knowledgeBaseService.GetKnowledgeBaseByID(ctx, id)
		if err != nil || kb == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "knowledge base not found"})
			return
		}
		if tenantID == 0 {
			tenantID = kb.TenantID
		} else if kb.TenantID != tenantID {
			c.JSON(http.StatusBadRequest, gin.H{"error": "knowledge bases must belong to one tenant"})
			return
		}
	}

	// The synthetic session carries only the derived tenant; it has no ID, is
	// never persisted, and cannot be recalled by any later request.
	session := &types.Session{TenantID: tenantID}
	qaRequest := &types.QARequest{
		Session:          session,
		Query:            query,
		KnowledgeBaseIDs: request.KnowledgeBaseIDs,
		Stateless:        true,
	}

	bus := event.NewEventBus()
	streamDone := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(streamDone) }) }
	write := func(payload any) {
		c.SSEvent("message", payload)
		c.Writer.Flush()
	}

	bus.On(event.EventAgentFinalAnswer, func(_ context.Context, evt event.Event) error {
		data, ok := evt.Data.(event.AgentFinalAnswerData)
		if !ok {
			return nil
		}
		if data.Content != "" {
			write(&types.StreamResponse{
				ResponseType: types.ResponseTypeAnswer,
				Content:      data.Content,
				Done:         data.Done,
			})
		}
		if data.Done {
			write(&types.StreamResponse{ResponseType: types.ResponseTypeComplete})
			finish()
		}
		return nil
	})
	bus.On(event.EventAgentReferences, func(_ context.Context, evt event.Event) error {
		data, ok := evt.Data.(event.AgentReferencesData)
		if !ok {
			return nil
		}
		references, _ := data.References.([]*types.SearchResult)
		write(&types.StreamResponse{
			ResponseType:        types.ResponseTypeReferences,
			KnowledgeReferences: references,
		})
		return nil
	})
	bus.On(event.EventError, func(_ context.Context, evt event.Event) error {
		data, ok := evt.Data.(event.ErrorData)
		if !ok {
			return nil
		}
		write(&types.StreamResponse{
			ResponseType: types.ResponseTypeError,
			Content:      data.Error,
		})
		finish()
		return nil
	})

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)

	go func() {
		defer finish()
		if err := h.sessionService.KnowledgeQA(ctx, qaRequest, bus); err != nil {
			if ctx.Err() != nil {
				// Client disconnect aborts provider work; the stream is gone.
				return
			}
			logger.Errorf(ctx, "stateless knowledge answer failed: %v", err)
			bus.Emit(ctx, event.Event{
				Type: event.EventError,
				Data: event.ErrorData{Error: "knowledge answer failed", Stage: "stateless_answer"},
			})
		}
	}()

	<-streamDone
}
