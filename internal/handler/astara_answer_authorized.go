package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const (
	astaraAuthorizedMaxQueryChars = 2000
	astaraAuthorizedMaxDocs       = 1000
	astaraAuthorizedContractV2    = 2
	// astaraAnswerMaxWait bounds the post-pipeline wait for the terminal
	// answer event before the response is rejected as incomplete.
	astaraAnswerMaxWait = 180 * time.Second
)

// astaraAuthorizedDocument is one entry in the caller-provided document
// allowlist. All wire fields are strings: tenant_id is the external Plane
// tenant identifier, revision is an opaque SHA-256 fingerprint. The handler
// parses tenant_id to uint64 for DB lookup; revision is never compared to an
// integer column.
type astaraAuthorizedDocument struct {
	TenantID        string `json:"tenant_id"`
	KnowledgeBaseID string `json:"knowledge_base_id"`
	KnowledgeID     string `json:"knowledge_id"`
	Revision        string `json:"revision"`
}

// parsedAuthorizedDocument holds the validated internal form after string
// parsing and DB verification.
type parsedAuthorizedDocument struct {
	TenantID        uint64
	KnowledgeBaseID string
	KnowledgeID     string
	Revision        string // opaque SHA-256 hex
}

// providerSnapshot captures the mutable provider state of a document at
// admission time. Post-generation revalidation compares every field.
type providerSnapshot struct {
	KnowledgeBaseID string
	SourceRevision  int64
	UpdatedAt       time.Time
	ParseStatus     string
	EnableStatus    string
	Deleted         bool
}

// astaraAnswerAuthorizedRequest is the closed authorized-answer body.
// Unknown fields are rejected at decode.
type astaraAnswerAuthorizedRequest struct {
	ContractVersion     int                        `json:"contract_version"`
	Query               string                     `json:"query"`
	Documents           []astaraAuthorizedDocument `json:"documents"`
	AuthorizationDigest string                     `json:"authorization_digest"`
}

// authorizedAnswerUsage reports token consumption for the single pipeline
// invocation. All counters are zero when the provider does not return usage.
type authorizedAnswerUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// astaraAnswerAuthorizedResponse is the bounded JSON contract returned by
// POST /api/v1/astara/answer-authorized.
type astaraAnswerAuthorizedResponse struct {
	ContractVersion     int                         `json:"contract_version"`
	AuthorizationDigest string                      `json:"authorization_digest"`
	Answer              string                      `json:"answer"`
	References          []authorizedAnswerReference `json:"references"`
	Usage               authorizedAnswerUsage       `json:"usage"`
}

// authorizedAnswerReference is one cited document chunk in the response.
// knowledge_base_id is mandatory — Plane validates the (kb, doc) pair.
type authorizedAnswerReference struct {
	KnowledgeBaseID string  `json:"knowledge_base_id"`
	KnowledgeID     string  `json:"knowledge_id"`
	KnowledgeTitle  string  `json:"knowledge_title,omitempty"`
	Content         string  `json:"content"`
	Score           float64 `json:"score,omitempty"`
	ChunkIndex      int     `json:"chunk_index,omitempty"`
}

// AstaraAnswerAuthorizedHandler serves POST /api/v1/astara/answer-authorized:
// pre-authorized RAG over a caller-provided document allowlist. Every document
// is validated against the database (existence, tenant ownership, KB
// membership) BEFORE the retrieval/rerank/context/generation pipeline runs.
// After generation, provider state is revalidated against a pre-generation
// snapshot. The tenant is derived from the validated documents — never from
// caller-trusted fields alone.
type AstaraAnswerAuthorizedHandler struct {
	db                   *gorm.DB
	sessionService       interfaces.SessionService
	knowledgeBaseService interfaces.KnowledgeBaseService
	knowledgeService     interfaces.KnowledgeService
	modelRepo            interfaces.ModelRepository
}

// NewAstaraAnswerAuthorizedHandler constructs the handler. All dependencies
// are required; nil-safe wiring is handled at the router layer.
func NewAstaraAnswerAuthorizedHandler(
	db *gorm.DB,
	sessionService interfaces.SessionService,
	knowledgeBaseService interfaces.KnowledgeBaseService,
	knowledgeService interfaces.KnowledgeService,
	modelRepo interfaces.ModelRepository,
) *AstaraAnswerAuthorizedHandler {
	return &AstaraAnswerAuthorizedHandler{
		db:                   db,
		sessionService:       sessionService,
		knowledgeBaseService: knowledgeBaseService,
		knowledgeService:     knowledgeService,
		modelRepo:            modelRepo,
	}
}

// AnswerAuthorized executes the pre-authorized RAG pipeline and returns a
// bounded JSON response (no SSE streaming).
func (h *AstaraAnswerAuthorizedHandler) AnswerAuthorized(c *gin.Context) {
	ctx := c.Request.Context()

	// ── 1. Decode & validate request shape ──────────────────────────────
	var req astaraAnswerAuthorizedRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 512*1024)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid authorized answer request"})
		return
	}

	if err := decoder.Decode(new(any)); err != io.EOF {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid authorized answer request"})
		return
	}

	if req.ContractVersion != astaraAuthorizedContractV2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "contract_version must be 2"})
		return
	}

	if req.Query != strings.TrimSpace(req.Query) || req.Query == "" || len([]rune(req.Query)) > astaraAuthorizedMaxQueryChars {
		c.JSON(http.StatusBadRequest, gin.H{"error": "query is required and bounded"})
		return
	}

	if len(req.Documents) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "documents is required and non-empty"})
		return
	}
	if len(req.Documents) > astaraAuthorizedMaxDocs {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("documents exceeds max %d", astaraAuthorizedMaxDocs)})
		return
	}

	// ── 2. Validate each document entry ─────────────────────────────────
	//    Reject empty/invalid fields and duplicate knowledge_ids.
	seenKnowledgeIDs := make(map[string]bool, len(req.Documents))
	for i, doc := range req.Documents {
		if doc.TenantID == "" || doc.TenantID != strings.TrimSpace(doc.TenantID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("documents[%d].tenant_id is required", i)})
			return
		}
		// Validate tenant_id is a numeric string.
		if _, err := strconv.ParseUint(doc.TenantID, 10, 64); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("documents[%d].tenant_id must be a numeric string", i)})
			return
		}
		doc.KnowledgeBaseID = strings.TrimSpace(doc.KnowledgeBaseID)
		if doc.KnowledgeBaseID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("documents[%d].knowledge_base_id is required", i)})
			return
		}
		doc.KnowledgeID = strings.TrimSpace(doc.KnowledgeID)
		if doc.KnowledgeID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("documents[%d].knowledge_id is required", i)})
			return
		}
		doc.Revision = strings.TrimSpace(doc.Revision)
		if doc.Revision == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("documents[%d].revision is required", i)})
			return
		}
		// Validate revision is a 64-char hex string (SHA-256).
		if len(doc.Revision) != 64 || !isHexString(doc.Revision) {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("documents[%d].revision must be a 64-char hex string", i)})
			return
		}
		if seenKnowledgeIDs[doc.KnowledgeID] {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("documents[%d].knowledge_id is duplicated", i)})
			return
		}
		seenKnowledgeIDs[doc.KnowledgeID] = true
		// Write back trimmed values for digest computation.
		req.Documents[i] = doc
	}

	// ── 3. Verify authorization_digest ──────────────────────────────────
	expectedDigest := computeAuthorizedDigest(req.Documents)
	if req.AuthorizationDigest != expectedDigest {
		c.JSON(http.StatusBadRequest, gin.H{"error": "authorization_digest mismatch"})
		return
	}

	// ── 4. Real doc allowlist: DB verification ──────────────────────────
	//    Every document must exist and belong to the claimed tenant/KB.
	//    All documents must belong to exactly one tenant (no mixed mapping).
	//    Provider state is snapshot for post-generation revalidation.
	var tenantID uint64
	knowledgeIDs := make([]string, 0, len(req.Documents))
	kbIDSet := make(map[string]bool, len(req.Documents))
	snapshots := make(map[string]*providerSnapshot, len(req.Documents))
	verifiedDocs := make([]parsedAuthorizedDocument, 0, len(req.Documents))

	for _, doc := range req.Documents {
		parsedTenant, _ := strconv.ParseUint(doc.TenantID, 10, 64) // already validated

		k, err := h.knowledgeService.GetKnowledgeByIDOnly(ctx, doc.KnowledgeID)
		if err != nil || k == nil {
			c.JSON(http.StatusNotFound, gin.H{
				"error":        "document not found",
				"knowledge_id": doc.KnowledgeID,
			})
			return
		}
		if k.DeletedAt.Valid || !k.DeletedAt.Time.IsZero() || k.ParseStatus != "completed" || k.EnableStatus != "enabled" {
			c.JSON(http.StatusConflict, gin.H{"error": "document is not queryable"})
			return
		}
		if k.TenantID != parsedTenant {
			c.JSON(http.StatusForbidden, gin.H{
				"error":        "tenant ownership mismatch",
				"knowledge_id": doc.KnowledgeID,
			})
			return
		}
		if k.KnowledgeBaseID != doc.KnowledgeBaseID {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":        "knowledge_base_id mismatch",
				"knowledge_id": doc.KnowledgeID,
			})
			return
		}
		// Mixed-mapping guard: all documents must belong to one tenant.
		if tenantID == 0 {
			tenantID = k.TenantID
		} else if k.TenantID != tenantID {
			c.JSON(http.StatusBadRequest, gin.H{"error": "documents must belong to one tenant"})
			return
		}

		// Snapshot provider state for post-generation revalidation.
		snapshots[doc.KnowledgeID] = &providerSnapshot{
			KnowledgeBaseID: k.KnowledgeBaseID,
			SourceRevision:  k.SourceRevision,
			UpdatedAt:       k.UpdatedAt,
			ParseStatus:     k.ParseStatus,
			EnableStatus:    k.EnableStatus,
			Deleted:         !k.DeletedAt.Time.IsZero(),
		}

		verifiedDocs = append(verifiedDocs, parsedAuthorizedDocument{
			TenantID:        parsedTenant,
			KnowledgeBaseID: doc.KnowledgeBaseID,
			KnowledgeID:     doc.KnowledgeID,
			Revision:        doc.Revision,
		})
		knowledgeIDs = append(knowledgeIDs, k.ID)
		if !kbIDSet[k.KnowledgeBaseID] {
			kbIDSet[k.KnowledgeBaseID] = true
		}
	}

	// ── 4.5 Plane-owned model configuration gate ────────────────────────
	//    Generation binds exclusively to the Plane-pushed KnowledgeQA
	//    vendor configuration; without an applied push there is no
	//    provider-local fallback model.
	planeModel, err := ResolvePlaneOwnedChatModel(ctx, h.modelRepo)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "knowledge model config read failed"})
		return
	}
	if planeModel == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "knowledge_model_config_not_applied"})
		return
	}

	// ── 5. Execute RAG pipeline ─────────────────────────────────────────
	//    KnowledgeBaseIDs: nil — passing full KB IDs would create
	//    SearchTargetTypeKnowledgeBase targets in buildSearchTargets,
	//    defeating the allowlist. Document-only KnowledgeIDs produce
	//    scoped SearchTargetTypeKnowledge targets instead.
	session := &types.Session{TenantID: tenantID}
	qaRequest := &types.QARequest{
		Session:          session,
		Query:            req.Query,
		KnowledgeBaseIDs: nil,
		KnowledgeIDs:     knowledgeIDs,
		Stateless:        true,
		// The chat model is the Plane-owned row exclusively; no tenant
		// model registry is consulted for generation.
		SummaryModelID: planeModel.ID,
	}

	bus := event.NewEventBus()
	var (
		mu            sync.Mutex
		answerParts   []string
		references    []authorizedAnswerReference
		usage         authorizedAnswerUsage
		pipelineErr   string
		bufferedBytes int
		answerDone    bool
	)

	bus.On(event.EventAgentFinalAnswer, func(_ context.Context, evt event.Event) error {
		data, ok := evt.Data.(event.AgentFinalAnswerData)
		if !ok {
			return nil
		}
		mu.Lock()
		defer mu.Unlock()
		if pipelineErr != "" {
			return nil
		}
		if bufferedBytes+len(data.Content) > 1024*1024 {
			pipelineErr = "authorized answer exceeds buffer limit"
			return nil
		}
		bufferedBytes += len(data.Content)
		if data.Content != "" {
			answerParts = append(answerParts, data.Content)
		}
		answerDone = answerDone || data.Done
		return nil
	})
	bus.On(event.EventAgentReferences, func(_ context.Context, evt event.Event) error {
		mu.Lock()
		defer mu.Unlock()
		data, ok := evt.Data.(event.AgentReferencesData)
		if !ok {
			pipelineErr = "invalid reference event"
			return nil
		}
		results, ok := data.References.([]*types.SearchResult)
		if !ok {
			pipelineErr = "invalid reference payload"
			return nil
		}
		for _, r := range results {
			if pipelineErr != "" {
				break
			}
			if len(references) >= 1000 {
				pipelineErr = "too many references"
				break
			}
			if r != nil {
				bufferedBytes += len(r.Content) + len(r.KnowledgeTitle)
				if bufferedBytes > 1024*1024 {
					pipelineErr = "authorized answer exceeds buffer limit"
					break
				}
			}
			if r == nil || snapshots[r.KnowledgeID] == nil || snapshots[r.KnowledgeID].KnowledgeBaseID != r.KnowledgeBaseID {
				pipelineErr = "reference outside authorized scope"
				continue
			}
			ref := authorizedAnswerReference{
				KnowledgeBaseID: r.KnowledgeBaseID,
				KnowledgeID:     r.KnowledgeID,
				KnowledgeTitle:  r.KnowledgeTitle,
				Content:         r.Content,
				Score:           r.Score,
				ChunkIndex:      r.ChunkIndex,
			}
			references = append(references, ref)
		}
		return nil
	})
	bus.On(event.EventError, func(_ context.Context, evt event.Event) error {
		data, ok := evt.Data.(event.ErrorData)
		if !ok {
			return nil
		}
		mu.Lock()
		defer mu.Unlock()
		// Errors are sticky: an empty later event cannot clear a scope or
		// buffer rejection and make the already tainted answer deliverable.
		if pipelineErr == "" {
			pipelineErr = "knowledge pipeline failed"
			if data.Error != "" {
				pipelineErr = data.Error
			}
		}
		return nil
	})

	// The provider pipeline resolves models through the tenant context;
	// inject the tenant derived from the validated documents (never from
	// caller-trusted fields) so the Plane-owned global model row is
	// visible without a tenant-local registry.
	ctx = context.WithValue(ctx, types.TenantIDContextKey, tenantID)
	if h.db != nil {
		var tenantRow types.Tenant
		if err := h.db.WithContext(ctx).First(&tenantRow, tenantID).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "tenant lookup failed"})
			return
		}
		ctx = context.WithValue(ctx, types.TenantInfoContextKey, &tenantRow)
	}
	if err := h.sessionService.KnowledgeQA(ctx, qaRequest, bus); err != nil {
		logger.Errorf(ctx, "authorized knowledge answer failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "knowledge answer failed"})
		return
	}

	// The stream stage consumes the model stream in a background goroutine;
	// KnowledgeQA returns once the pipeline stages complete, which can be
	// before the final answer event arrives. Wait for the terminal answer
	// event (bounded) while the request context stays alive.
	answerDeadline := time.Now().Add(astaraAnswerMaxWait)
	for {
		mu.Lock()
		done := answerDone || pipelineErr != ""
		mu.Unlock()
		if done || time.Now().After(answerDeadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	mu.Lock()
	if pipelineErr != "" || !answerDone {
		mu.Unlock()
		c.JSON(http.StatusBadGateway, gin.H{"error": "authorized answer incomplete or rejected"})
		return
	}
	answer := strings.Join(answerParts, "")
	collectedRefs := make([]authorizedAnswerReference, len(references))
	copy(collectedRefs, references)
	mu.Unlock()

	// ── 6. Post-generation revalidation ─────────────────────────────────
	//    Compare provider state against the pre-generation snapshot.
	//    sourceRevision, updatedAt, parseStatus, enableStatus, deleted.
	for _, doc := range verifiedDocs {
		snap := snapshots[doc.KnowledgeID]
		if snap == nil {
			// Should not happen — admission created the snapshot.
			c.JSON(http.StatusConflict, gin.H{
				"error":        "internal: missing snapshot",
				"knowledge_id": doc.KnowledgeID,
			})
			return
		}
		k, err := h.knowledgeService.GetKnowledgeByIDOnly(ctx, doc.KnowledgeID)
		if err != nil || k == nil {
			logger.Warnf(ctx, "post-generation revalidation: document %s disappeared", doc.KnowledgeID)
			c.JSON(http.StatusConflict, gin.H{
				"error":        "document changed during generation",
				"knowledge_id": doc.KnowledgeID,
			})
			return
		}
		if k.TenantID != doc.TenantID || k.KnowledgeBaseID != doc.KnowledgeBaseID {
			logger.Warnf(ctx, "post-generation revalidation: document %s ownership changed", doc.KnowledgeID)
			c.JSON(http.StatusConflict, gin.H{
				"error":        "document ownership changed during generation",
				"knowledge_id": doc.KnowledgeID,
			})
			return
		}
		if k.SourceRevision != snap.SourceRevision {
			logger.Warnf(ctx, "post-generation revalidation: document %s sourceRevision changed", doc.KnowledgeID)
			c.JSON(http.StatusConflict, gin.H{
				"error":        "document changed during generation",
				"knowledge_id": doc.KnowledgeID,
			})
			return
		}
		if !k.UpdatedAt.Equal(snap.UpdatedAt) {
			logger.Warnf(ctx, "post-generation revalidation: document %s updatedAt changed", doc.KnowledgeID)
			c.JSON(http.StatusConflict, gin.H{
				"error":        "document changed during generation",
				"knowledge_id": doc.KnowledgeID,
			})
			return
		}
		if k.ParseStatus != snap.ParseStatus {
			logger.Warnf(ctx, "post-generation revalidation: document %s parseStatus changed", doc.KnowledgeID)
			c.JSON(http.StatusConflict, gin.H{
				"error":        "document changed during generation",
				"knowledge_id": doc.KnowledgeID,
			})
			return
		}
		if k.EnableStatus != snap.EnableStatus {
			logger.Warnf(ctx, "post-generation revalidation: document %s enableStatus changed", doc.KnowledgeID)
			c.JSON(http.StatusConflict, gin.H{
				"error":        "document changed during generation",
				"knowledge_id": doc.KnowledgeID,
			})
			return
		}
		if !snap.Deleted && !k.DeletedAt.Time.IsZero() {
			logger.Warnf(ctx, "post-generation revalidation: document %s was deleted", doc.KnowledgeID)
			c.JSON(http.StatusConflict, gin.H{
				"error":        "document changed during generation",
				"knowledge_id": doc.KnowledgeID,
			})
			return
		}
	}

	// ── 7. Emit bounded JSON response ──────────────────────────────────
	payload, err := json.Marshal(astaraAnswerAuthorizedResponse{
		ContractVersion:     astaraAuthorizedContractV2,
		AuthorizationDigest: expectedDigest,
		Answer:              answer,
		References:          collectedRefs,
		Usage:               usage,
	})
	// JSON escaping and metadata count against the wire budget too.
	if err != nil || len(payload) > 2*1024*1024 {
		c.JSON(http.StatusBadGateway, gin.H{"error": "authorized answer encoding rejected"})
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", payload)
}

// computeAuthorizedDigest returns the sha256 hex of the canonical JSON
// representation of the documents array. Canonical ordering is by
// (tenant_id, knowledge_base_id, knowledge_id, revision) ascending, with
// the JSON encoding using stable key order. All values are strings.
func computeAuthorizedDigest(docs []astaraAuthorizedDocument) string {
	// Sort a copy to avoid mutating the caller's slice.
	sorted := make([]astaraAuthorizedDocument, len(docs))
	copy(sorted, docs)
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.TenantID != b.TenantID {
			return a.TenantID < b.TenantID
		}
		if a.KnowledgeBaseID != b.KnowledgeBaseID {
			return a.KnowledgeBaseID < b.KnowledgeBaseID
		}
		if a.KnowledgeID != b.KnowledgeID {
			return a.KnowledgeID < b.KnowledgeID
		}
		if a.Revision != b.Revision {
			return a.Revision < b.Revision
		}
		return a.TenantID < b.TenantID
	})

	// Use JSON escaping, not Go %q escaping. Match the search contract and
	// Plane's UTF-8 canonical encoding, including U+2028/U+2029 escapes.
	canonical := make([]map[string]string, 0, len(sorted))
	for _, doc := range sorted {
		canonical = append(canonical, map[string]string{
			"knowledge_base_id": doc.KnowledgeBaseID, "knowledge_id": doc.KnowledgeID,
			"revision": doc.Revision, "tenant_id": doc.TenantID,
		})
	}
	var buf strings.Builder
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(canonical); err != nil {
		return ""
	}
	hash := sha256.Sum256([]byte(strings.TrimSuffix(buf.String(), "\n")))
	return hex.EncodeToString(hash[:])
}

// isHexString returns true if s is non-empty and consists only of [0-9a-fA-F].
func isHexString(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
