package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// stubAnswerSessionService embeds the full interface so only KnowledgeQA is
// implemented; any other call would panic the test rather than pass silently.
type stubAnswerSessionService struct {
	interfaces.SessionService
	calls []*types.QARequest
	run   func(ctx context.Context, req *types.QARequest, bus *event.EventBus)
}

func (s *stubAnswerSessionService) KnowledgeQA(ctx context.Context, req *types.QARequest, bus *event.EventBus) error {
	s.calls = append(s.calls, req)
	if s.run != nil {
		s.run(ctx, req, bus)
	}
	return nil
}

type stubAnswerKnowledgeBaseService struct {
	interfaces.KnowledgeBaseService
	bases map[string]*types.KnowledgeBase
}

func (s *stubAnswerKnowledgeBaseService) GetKnowledgeBaseByID(_ context.Context, id string) (*types.KnowledgeBase, error) {
	return s.bases[id], nil
}

func answerTestEngine(t *testing.T, session *stubAnswerSessionService, kb *stubAnswerKnowledgeBaseService) *gin.Engine {
	t.Helper()
	t.Setenv(astaraServiceAuthEnv, "test-service-secret")
	h := NewAstaraAnswerHandler(session, kb)
	r := gin.New()
	r.Group("/api/v1/astara", AstaraServiceAuth).POST("/answer", h.Answer)
	return r
}

func answerRequest(t *testing.T, engine *gin.Engine, body any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/astara/answer", bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-service-secret")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	return recorder
}

func TestStatelessAnswerRejectsAuthorityBearingFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	kb := &stubAnswerKnowledgeBaseService{bases: map[string]*types.KnowledgeBase{
		"kb-1": {ID: "kb-1", TenantID: 7},
	}}
	for _, body := range []map[string]any{
		{"query": "q", "knowledge_base_ids": []string{"kb-1"}, "agent_id": "a"},
		{"query": "q", "knowledge_base_ids": []string{"kb-1"}, "session_id": "s"},
		{"query": "q", "knowledge_base_ids": []string{"kb-1"}, "web_search": true},
		{"query": "q", "knowledge_base_ids": []string{"kb-1"}, "model": "gpt-x"},
		{"query": "q", "knowledge_base_ids": []string{"kb-1"}, "endpoint": "https://evil"},
		{"query": "q", "knowledge_base_ids": []string{"kb-1"}, "tenant_id": 9},
	} {
		engine := answerTestEngine(t, &stubAnswerSessionService{}, kb)
		recorder := answerRequest(t, engine, body)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body=%v status=%d, want 400", body, recorder.Code)
		}
	}
}

func TestStatelessAnswerRequiresBoundedScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	kb := &stubAnswerKnowledgeBaseService{bases: map[string]*types.KnowledgeBase{
		"kb-1": {ID: "kb-1", TenantID: 7},
	}}
	engine := answerTestEngine(t, &stubAnswerSessionService{}, kb)
	for _, body := range []map[string]any{
		{"query": "q"},
		{"query": "", "knowledge_base_ids": []string{"kb-1"}},
		{"query": strings.Repeat("x", 2001), "knowledge_base_ids": []string{"kb-1"}},
		{"query": "q", "knowledge_base_ids": []string{"missing-kb"}},
		{"query": "q", "knowledge_base_ids": []string{"kb-1", "kb-1"}},
	} {
		recorder := answerRequest(t, engine, body)
		if recorder.Code != http.StatusBadRequest && recorder.Code != http.StatusNotFound {
			t.Fatalf("body=%v status=%d, want 400/404", body, recorder.Code)
		}
	}
}

func TestStatelessAnswerDerivesTenantFromKnowledgeBases(t *testing.T) {
	gin.SetMode(gin.TestMode)
	kb := &stubAnswerKnowledgeBaseService{bases: map[string]*types.KnowledgeBase{
		"kb-1": {ID: "kb-1", TenantID: 7},
		"kb-2": {ID: "kb-2", TenantID: 7},
	}}
	session := &stubAnswerSessionService{}
	engine := answerTestEngine(t, session, kb)
	recorder := answerRequest(t, engine, map[string]any{
		"query":             "explain the policy",
		"knowledge_base_ids": []string{"kb-1", "kb-2"},
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(session.calls) != 1 {
		t.Fatalf("KnowledgeQA calls=%d, want 1", len(session.calls))
	}
	req := session.calls[0]
	if !req.Stateless {
		t.Fatal("QARequest.Stateless must be true")
	}
	if req.Session == nil || req.Session.TenantID != 7 || req.Session.ID != "" {
		t.Fatalf("synthetic session=%+v, want tenant 7 with no session id", req.Session)
	}
	if req.WebSearchEnabled {
		t.Fatal("web search must be structurally disabled")
	}
}

func TestStatelessAnswerRejectsCrossTenantScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	kb := &stubAnswerKnowledgeBaseService{bases: map[string]*types.KnowledgeBase{
		"kb-1": {ID: "kb-1", TenantID: 7},
		"kb-2": {ID: "kb-2", TenantID: 8},
	}}
	engine := answerTestEngine(t, &stubAnswerSessionService{}, kb)
	recorder := answerRequest(t, engine, map[string]any{
		"query":             "q",
		"knowledge_base_ids": []string{"kb-1", "kb-2"},
	})
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", recorder.Code)
	}
}

func TestStatelessAnswerStreamsAnswerReferencesAndComplete(t *testing.T) {
	gin.SetMode(gin.TestMode)
	kb := &stubAnswerKnowledgeBaseService{bases: map[string]*types.KnowledgeBase{
		"kb-1": {ID: "kb-1", TenantID: 7},
	}}
	session := &stubAnswerSessionService{
		run: func(ctx context.Context, _ *types.QARequest, bus *event.EventBus) {
			bus.Emit(ctx, event.Event{
				Type: event.EventAgentReferences,
				Data: event.AgentReferencesData{References: []*types.SearchResult{{KnowledgeID: "doc-1", Content: "passage"}}},
			})
			bus.Emit(ctx, event.Event{Type: event.EventAgentFinalAnswer, Data: event.AgentFinalAnswerData{Content: "hello "}})
			bus.Emit(ctx, event.Event{Type: event.EventAgentFinalAnswer, Data: event.AgentFinalAnswerData{Content: "world", Done: true}})
		},
	}
	engine := answerTestEngine(t, session, kb)
	recorder := answerRequest(t, engine, map[string]any{
		"query":             "q",
		"knowledge_base_ids": []string{"kb-1"},
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, `"response_type":"references"`) {
		t.Fatalf("missing references event: %s", body)
	}
	if !strings.Contains(body, `"response_type":"answer"`) {
		t.Fatalf("missing answer event: %s", body)
	}
	if !strings.Contains(body, `"response_type":"complete"`) {
		t.Fatalf("missing complete event: %s", body)
	}
	if !strings.Contains(body, `"knowledge_id":"doc-1"`) {
		t.Fatalf("missing provider reference identity: %s", body)
	}
}

func TestStatelessAnswerErrorEventFailsTheStream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	kb := &stubAnswerKnowledgeBaseService{bases: map[string]*types.KnowledgeBase{
		"kb-1": {ID: "kb-1", TenantID: 7},
	}}
	session := &stubAnswerSessionService{
		run: func(ctx context.Context, _ *types.QARequest, bus *event.EventBus) {
			bus.Emit(ctx, event.Event{Type: event.EventError, Data: event.ErrorData{Error: "provider exploded"}})
		},
	}
	engine := answerTestEngine(t, session, kb)
	recorder := answerRequest(t, engine, map[string]any{
		"query":             "q",
		"knowledge_base_ids": []string{"kb-1"},
	})
	if !strings.Contains(recorder.Body.String(), `"response_type":"error"`) {
		t.Fatalf("missing error event: %s", recorder.Body.String())
	}
}

func TestStatelessAnswerRequiresServiceAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv(astaraServiceAuthEnv, "test-service-secret")
	h := NewAstaraAnswerHandler(&stubAnswerSessionService{}, &stubAnswerKnowledgeBaseService{})
	r := gin.New()
	r.Group("/api/v1/astara", AstaraServiceAuth).POST("/answer", h.Answer)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/astara/answer", bytes.NewReader([]byte("{}")))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", recorder.Code)
	}
}
