package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ── Stubs ────────────────────────────────────────────────────────────────────

type stubAuthorizedSessionService struct {
	interfaces.SessionService
	calls []*types.QARequest
	run   func(ctx context.Context, req *types.QARequest, bus *event.EventBus)
}

func (s *stubAuthorizedSessionService) KnowledgeQA(ctx context.Context, req *types.QARequest, bus *event.EventBus) error {
	s.calls = append(s.calls, req)
	if s.run != nil {
		s.run(ctx, req, bus)
	}
	return nil
}

type stubAuthorizedKnowledgeBaseService struct {
	interfaces.KnowledgeBaseService
	bases map[string]*types.KnowledgeBase
}

func (s *stubAuthorizedKnowledgeBaseService) GetKnowledgeBaseByID(_ context.Context, id string) (*types.KnowledgeBase, error) {
	return s.bases[id], nil
}

type stubAuthorizedKnowledgeService struct {
	interfaces.KnowledgeService
	docs map[string]*types.Knowledge
}

func (s *stubAuthorizedKnowledgeService) GetKnowledgeByIDOnly(_ context.Context, id string) (*types.Knowledge, error) {
	k := s.docs[id]
	if k == nil {
		return nil, nil
	}
	return k, nil
}

// stubAuthorizedModelRepo is an in-memory ModelRepository for the plane-owned
// KnowledgeQA row used by the answer admission gate.
type stubAuthorizedModelRepo struct {
	interfaces.ModelRepository
	rows map[string]*types.Model
}

func (s *stubAuthorizedModelRepo) Create(_ context.Context, m *types.Model) error {
	if s.rows == nil {
		s.rows = map[string]*types.Model{}
	}
	s.rows[m.ID] = m
	return nil
}

func (s *stubAuthorizedModelRepo) GetByID(_ context.Context, _ uint64, id string) (*types.Model, error) {
	return s.rows[id], nil
}

func (s *stubAuthorizedModelRepo) List(_ context.Context, _ uint64, _ types.ModelType, _ types.ModelSource) ([]*types.Model, error) {
	return nil, nil
}

func (s *stubAuthorizedModelRepo) Update(_ context.Context, m *types.Model) error {
	s.rows[m.ID] = m
	return nil
}

func (s *stubAuthorizedModelRepo) Delete(_ context.Context, _ uint64, id string) error {
	delete(s.rows, id)
	return nil
}

func (s *stubAuthorizedModelRepo) ClearDefaultByType(_ context.Context, _ uint, _ types.ModelType, _ string) error {
	return nil
}

// appliedPlaneModel returns the applied plane-owned KnowledgeQA row used by
// tests that expect generation to proceed.
func appliedPlaneModel() *types.Model {
	return &types.Model{
		ID:          types.PlaneOwnedKnowledgeQAModelID,
		TenantID:    0,
		Name:        "plane-qa",
		DisplayName: "plane-qa",
		Type:        types.ModelTypeKnowledgeQA,
		Source:      types.ModelSourceOpenAI,
		IsBuiltin:   true,
		ManagedBy:   types.PlaneManagedBy,
		Status:      types.ModelStatusActive,
		Parameters: types.ModelParameters{
			BaseURL:  "https://models.example.invalid/v1",
			APIKey:   "test-key",
			Provider: "openai-compatible",
		},
	}
}

// ── Helpers ──────────────────────────────────────────────────────────────────

// knownRevision is a fixed 64-char hex string used across tests.
const knownRevision = "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"

func authorizedTestEngine(t *testing.T, session *stubAuthorizedSessionService, kb *stubAuthorizedKnowledgeBaseService, ks *stubAuthorizedKnowledgeService) *gin.Engine {
	t.Helper()
	t.Setenv(astaraServiceAuthEnv, "test-service-secret")
	repo := &stubAuthorizedModelRepo{rows: map[string]*types.Model{
		types.PlaneOwnedKnowledgeQAModelID: appliedPlaneModel(),
	}}
	h := NewAstaraAnswerAuthorizedHandler(nil, session, kb, ks, repo)
	r := gin.New()
	r.Group("/api/v1/astara", AstaraServiceAuth).POST("/answer-authorized", h.AnswerAuthorized)
	return r
}

func authorizedRequest(t *testing.T, engine *gin.Engine, body any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/astara/answer-authorized", bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-service-secret")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	return recorder
}

// makeValidAuthorizedBody returns a valid request body with correct digest.
func makeValidAuthorizedBody(docs []astaraAuthorizedDocument) map[string]any {
	return map[string]any{
		"contract_version":     2,
		"query":                "explain the policy",
		"documents":            docs,
		"authorization_digest": computeAuthorizedDigest(docs),
	}
}

// makeKnowledge creates a test Knowledge entry with the given fields.
func makeKnowledge(id string, tenantID uint64, kbID string) *types.Knowledge {
	return &types.Knowledge{
		ID:              id,
		TenantID:        tenantID,
		KnowledgeBaseID: kbID,
		ParseStatus:     "completed",
		EnableStatus:    "enabled",
		UpdatedAt:       time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// ── Tests ────────────────────────────────────────────────────────────────────

func TestAuthorizedAnswerRejectsUnqueryableDocumentsBeforeGeneration(t *testing.T) {
	for _, state := range []string{"deleted", "failed", "pending", "disabled"} {
		t.Run(state, func(t *testing.T) {
			k := makeKnowledge("k-1", 7, "kb-1")
			switch state {
			case "deleted":
				k.DeletedAt.Valid = true
				k.DeletedAt.Time = time.Now()
			case "disabled":
				k.EnableStatus = "disabled"
			default:
				k.ParseStatus = state
			}
			ks := &stubAuthorizedKnowledgeService{docs: map[string]*types.Knowledge{"k-1": k}}
			kb := &stubAuthorizedKnowledgeBaseService{bases: map[string]*types.KnowledgeBase{"kb-1": {ID: "kb-1", TenantID: 7}}}
			session := &stubAuthorizedSessionService{}
			recorder := authorizedRequest(t, authorizedTestEngine(t, session, kb, ks), makeValidAuthorizedBody([]astaraAuthorizedDocument{{TenantID: "7", KnowledgeBaseID: "kb-1", KnowledgeID: "k-1", Revision: knownRevision}}))
			if recorder.Code != http.StatusConflict || len(session.calls) != 0 {
				t.Fatalf("unqueryable source reached generation: status=%d calls=%d", recorder.Code, len(session.calls))
			}
		})
	}
}

func TestAuthorizedAnswerRequiresServiceAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv(astaraServiceAuthEnv, "test-service-secret")
	h := NewAstaraAnswerAuthorizedHandler(nil, &stubAuthorizedSessionService{}, &stubAuthorizedKnowledgeBaseService{}, &stubAuthorizedKnowledgeService{}, &stubAuthorizedModelRepo{rows: map[string]*types.Model{
		types.PlaneOwnedKnowledgeQAModelID: appliedPlaneModel(),
	}})
	r := gin.New()
	r.Group("/api/v1/astara", AstaraServiceAuth).POST("/answer-authorized", h.AnswerAuthorized)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/astara/answer-authorized", bytes.NewReader([]byte("{}")))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", recorder.Code)
	}
}

func TestAuthorizedAnswerRejectsTrailingAndOversizeJSON(t *testing.T) {
	for _, body := range []string{`{"contract_version":2} {"extra":true}`, strings.Repeat(" ", 512*1024+1) + `{}`} {
		session := &stubAuthorizedSessionService{}
		engine := authorizedTestEngine(t, session, &stubAuthorizedKnowledgeBaseService{}, &stubAuthorizedKnowledgeService{})
		recorder := authorizedRequest(t, engine, body)
		if recorder.Code != http.StatusBadRequest || len(session.calls) != 0 {
			t.Fatalf("unbounded input admitted: %d", recorder.Code)
		}
	}
}

func TestAuthorizedAnswerRejectsUnknownFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ks := &stubAuthorizedKnowledgeService{docs: map[string]*types.Knowledge{
		"k-1": makeKnowledge("k-1", 7, "kb-1"),
	}}
	kb := &stubAuthorizedKnowledgeBaseService{bases: map[string]*types.KnowledgeBase{
		"kb-1": {ID: "kb-1", TenantID: 7},
	}}
	engine := authorizedTestEngine(t, &stubAuthorizedSessionService{}, kb, ks)
	body := map[string]any{
		"contract_version":     2,
		"query":                "q",
		"documents":            []astaraAuthorizedDocument{{TenantID: "7", KnowledgeBaseID: "kb-1", KnowledgeID: "k-1", Revision: knownRevision}},
		"authorization_digest": "x",
		"agent_id":             "a",
	}
	recorder := authorizedRequest(t, engine, body)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestAuthorizedAnswerRejectsWrongContractVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := authorizedTestEngine(t, &stubAuthorizedSessionService{}, &stubAuthorizedKnowledgeBaseService{}, &stubAuthorizedKnowledgeService{})
	body := map[string]any{
		"contract_version": 1,
		"query":            "q",
		"documents": []map[string]any{
			{"tenant_id": "7", "knowledge_base_id": "kb-1", "knowledge_id": "k-1", "revision": knownRevision},
		},
	}
	recorder := authorizedRequest(t, engine, body)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", recorder.Code)
	}
}

func TestAuthorizedAnswerRejectsPaddedQuery(t *testing.T) {
	engine := authorizedTestEngine(t, &stubAuthorizedSessionService{}, &stubAuthorizedKnowledgeBaseService{}, &stubAuthorizedKnowledgeService{})
	recorder := authorizedRequest(t, engine, `{"contract_version":2,"query":" q ","documents":[],"authorization_digest":""}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", recorder.Code)
	}
}

func TestAuthorizedAnswerRejectsEmptyQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := authorizedTestEngine(t, &stubAuthorizedSessionService{}, &stubAuthorizedKnowledgeBaseService{}, &stubAuthorizedKnowledgeService{})
	body := map[string]any{
		"contract_version": 2,
		"query":            "",
		"documents": []map[string]any{
			{"tenant_id": "7", "knowledge_base_id": "kb-1", "knowledge_id": "k-1", "revision": knownRevision},
		},
	}
	recorder := authorizedRequest(t, engine, body)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", recorder.Code)
	}
}

func TestAuthorizedAnswerRejectsEmptyDocuments(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := authorizedTestEngine(t, &stubAuthorizedSessionService{}, &stubAuthorizedKnowledgeBaseService{}, &stubAuthorizedKnowledgeService{})
	body := map[string]any{
		"contract_version": 2,
		"query":            "q",
		"documents":        []map[string]any{},
	}
	recorder := authorizedRequest(t, engine, body)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", recorder.Code)
	}
}

func TestAuthorizedAnswerRejectsNonNumericTenantID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := authorizedTestEngine(t, &stubAuthorizedSessionService{}, &stubAuthorizedKnowledgeBaseService{}, &stubAuthorizedKnowledgeService{})
	body := map[string]any{
		"contract_version": 2,
		"query":            "q",
		"documents": []map[string]any{
			{"tenant_id": "not-a-number", "knowledge_base_id": "kb-1", "knowledge_id": "k-1", "revision": knownRevision},
		},
	}
	recorder := authorizedRequest(t, engine, body)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", recorder.Code)
	}
}

func TestAuthorizedAnswerRejectsNonHexRevision(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := authorizedTestEngine(t, &stubAuthorizedSessionService{}, &stubAuthorizedKnowledgeBaseService{}, &stubAuthorizedKnowledgeService{})
	body := map[string]any{
		"contract_version": 2,
		"query":            "q",
		"documents": []map[string]any{
			{"tenant_id": "7", "knowledge_base_id": "kb-1", "knowledge_id": "k-1", "revision": strings.Repeat("z", 64)},
		},
	}
	recorder := authorizedRequest(t, engine, body)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", recorder.Code)
	}
}

func TestAuthorizedAnswerRejectsShortRevision(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := authorizedTestEngine(t, &stubAuthorizedSessionService{}, &stubAuthorizedKnowledgeBaseService{}, &stubAuthorizedKnowledgeService{})
	body := map[string]any{
		"contract_version": 2,
		"query":            "q",
		"documents": []map[string]any{
			{"tenant_id": "7", "knowledge_base_id": "kb-1", "knowledge_id": "k-1", "revision": "abcd1234"},
		},
	}
	recorder := authorizedRequest(t, engine, body)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", recorder.Code)
	}
}

func TestAuthorizedAnswerRejectsDuplicateKnowledgeIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := authorizedTestEngine(t, &stubAuthorizedSessionService{}, &stubAuthorizedKnowledgeBaseService{}, &stubAuthorizedKnowledgeService{})
	body := map[string]any{
		"contract_version": 2,
		"query":            "q",
		"documents": []map[string]any{
			{"tenant_id": "7", "knowledge_base_id": "kb-1", "knowledge_id": "k-1", "revision": knownRevision},
			{"tenant_id": "7", "knowledge_base_id": "kb-1", "knowledge_id": "k-1", "revision": knownRevision},
		},
	}
	recorder := authorizedRequest(t, engine, body)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", recorder.Code)
	}
}

func TestAuthorizedAnswerRejectsDigestMismatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ks := &stubAuthorizedKnowledgeService{docs: map[string]*types.Knowledge{
		"k-1": makeKnowledge("k-1", 7, "kb-1"),
	}}
	kb := &stubAuthorizedKnowledgeBaseService{bases: map[string]*types.KnowledgeBase{
		"kb-1": {ID: "kb-1", TenantID: 7},
	}}
	engine := authorizedTestEngine(t, &stubAuthorizedSessionService{}, kb, ks)
	body := map[string]any{
		"contract_version":     2,
		"query":                "q",
		"documents":            []astaraAuthorizedDocument{{TenantID: "7", KnowledgeBaseID: "kb-1", KnowledgeID: "k-1", Revision: knownRevision}},
		"authorization_digest": "0000000000000000000000000000000000000000000000000000000000000000",
	}
	recorder := authorizedRequest(t, engine, body)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", recorder.Code)
	}
}

func TestAuthorizedAnswerRejectsMissingDocument(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ks := &stubAuthorizedKnowledgeService{docs: map[string]*types.Knowledge{}}
	kb := &stubAuthorizedKnowledgeBaseService{bases: map[string]*types.KnowledgeBase{
		"kb-1": {ID: "kb-1", TenantID: 7},
	}}
	engine := authorizedTestEngine(t, &stubAuthorizedSessionService{}, kb, ks)
	docs := []astaraAuthorizedDocument{{TenantID: "7", KnowledgeBaseID: "kb-1", KnowledgeID: "missing", Revision: knownRevision}}
	body := makeValidAuthorizedBody(docs)
	recorder := authorizedRequest(t, engine, body)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", recorder.Code)
	}
}

func TestAuthorizedAnswerRejectsTenantMismatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ks := &stubAuthorizedKnowledgeService{docs: map[string]*types.Knowledge{
		"k-1": makeKnowledge("k-1", 99, "kb-1"),
	}}
	kb := &stubAuthorizedKnowledgeBaseService{bases: map[string]*types.KnowledgeBase{
		"kb-1": {ID: "kb-1", TenantID: 99},
	}}
	engine := authorizedTestEngine(t, &stubAuthorizedSessionService{}, kb, ks)
	docs := []astaraAuthorizedDocument{{TenantID: "7", KnowledgeBaseID: "kb-1", KnowledgeID: "k-1", Revision: knownRevision}}
	body := makeValidAuthorizedBody(docs)
	recorder := authorizedRequest(t, engine, body)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", recorder.Code)
	}
}

func TestAuthorizedAnswerRejectsKBMismatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ks := &stubAuthorizedKnowledgeService{docs: map[string]*types.Knowledge{
		"k-1": makeKnowledge("k-1", 7, "kb-2"),
	}}
	kb := &stubAuthorizedKnowledgeBaseService{bases: map[string]*types.KnowledgeBase{
		"kb-1": {ID: "kb-1", TenantID: 7},
	}}
	engine := authorizedTestEngine(t, &stubAuthorizedSessionService{}, kb, ks)
	docs := []astaraAuthorizedDocument{{TenantID: "7", KnowledgeBaseID: "kb-1", KnowledgeID: "k-1", Revision: knownRevision}}
	body := makeValidAuthorizedBody(docs)
	recorder := authorizedRequest(t, engine, body)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", recorder.Code)
	}
}

func TestAuthorizedAnswerRejectsCrossTenantDocuments(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ks := &stubAuthorizedKnowledgeService{docs: map[string]*types.Knowledge{
		"k-1": makeKnowledge("k-1", 7, "kb-1"),
		"k-2": makeKnowledge("k-2", 8, "kb-2"),
	}}
	kb := &stubAuthorizedKnowledgeBaseService{bases: map[string]*types.KnowledgeBase{
		"kb-1": {ID: "kb-1", TenantID: 7},
		"kb-2": {ID: "kb-2", TenantID: 8},
	}}
	engine := authorizedTestEngine(t, &stubAuthorizedSessionService{}, kb, ks)
	docs := []astaraAuthorizedDocument{
		{TenantID: "7", KnowledgeBaseID: "kb-1", KnowledgeID: "k-1", Revision: knownRevision},
		{TenantID: "8", KnowledgeBaseID: "kb-2", KnowledgeID: "k-2", Revision: knownRevision},
	}
	body := makeValidAuthorizedBody(docs)
	recorder := authorizedRequest(t, engine, body)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", recorder.Code)
	}
}

func TestAuthorizedAnswerSuccessWithValidDocuments(t *testing.T) {
	gin.SetMode(gin.TestMode)
	k1 := makeKnowledge("k-1", 7, "kb-1")
	k2 := makeKnowledge("k-2", 7, "kb-2")
	ks := &stubAuthorizedKnowledgeService{docs: map[string]*types.Knowledge{
		"k-1": k1,
		"k-2": k2,
	}}
	kb := &stubAuthorizedKnowledgeBaseService{bases: map[string]*types.KnowledgeBase{
		"kb-1": {ID: "kb-1", TenantID: 7},
		"kb-2": {ID: "kb-2", TenantID: 7},
	}}
	session := &stubAuthorizedSessionService{
		run: func(ctx context.Context, _ *types.QARequest, bus *event.EventBus) {
			bus.Emit(ctx, event.Event{
				Type: event.EventAgentReferences,
				Data: event.AgentReferencesData{
					References: []*types.SearchResult{
						{KnowledgeBaseID: "kb-1", KnowledgeID: "k-1", KnowledgeTitle: "Doc A", Content: "passage one", Score: 0.95, ChunkIndex: 0},
						{KnowledgeBaseID: "kb-2", KnowledgeID: "k-2", KnowledgeTitle: "Doc B", Content: "passage two", Score: 0.88, ChunkIndex: 1},
					},
				},
			})
			bus.Emit(ctx, event.Event{Type: event.EventAgentFinalAnswer, Data: event.AgentFinalAnswerData{Content: "The policy states "}})
			bus.Emit(ctx, event.Event{Type: event.EventAgentFinalAnswer, Data: event.AgentFinalAnswerData{Content: "that all docs are valid.", Done: true}})
		},
	}
	engine := authorizedTestEngine(t, session, kb, ks)
	docs := []astaraAuthorizedDocument{
		{TenantID: "7", KnowledgeBaseID: "kb-1", KnowledgeID: "k-1", Revision: knownRevision},
		{TenantID: "7", KnowledgeBaseID: "kb-2", KnowledgeID: "k-2", Revision: knownRevision},
	}
	body := makeValidAuthorizedBody(docs)
	recorder := authorizedRequest(t, engine, body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	var resp astaraAnswerAuthorizedResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ContractVersion != 2 {
		t.Fatalf("contract_version=%d, want 2", resp.ContractVersion)
	}
	if resp.Answer != "The policy states that all docs are valid." {
		t.Fatalf("answer=%q", resp.Answer)
	}
	if len(resp.References) != 2 {
		t.Fatalf("references=%d, want 2", len(resp.References))
	}
	if resp.References[0].KnowledgeBaseID != "kb-1" {
		t.Fatalf("ref[0].knowledge_base_id=%q, want kb-1", resp.References[0].KnowledgeBaseID)
	}
	if resp.References[0].KnowledgeID != "k-1" {
		t.Fatalf("ref[0].knowledge_id=%q, want k-1", resp.References[0].KnowledgeID)
	}
	if resp.References[1].KnowledgeBaseID != "kb-2" {
		t.Fatalf("ref[1].knowledge_base_id=%q, want kb-2", resp.References[1].KnowledgeBaseID)
	}
	if resp.AuthorizationDigest == "" {
		t.Fatal("authorization_digest is empty")
	}
	if len(session.calls) != 1 {
		t.Fatalf("KnowledgeQA calls=%d, want 1", len(session.calls))
	}
	if len(session.calls[0].KnowledgeBaseIDs) != 0 {
		t.Fatalf("KnowledgeBaseIDs=%v, want nil", session.calls[0].KnowledgeBaseIDs)
	}
	if !session.calls[0].Stateless {
		t.Fatal("QARequest.Stateless must be true")
	}
}

func TestAuthorizedAnswerUNIONKnowledgeIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ks := &stubAuthorizedKnowledgeService{docs: map[string]*types.Knowledge{
		"k-1": makeKnowledge("k-1", 7, "kb-a"),
		"k-2": makeKnowledge("k-2", 7, "kb-b"),
		"k-3": makeKnowledge("k-3", 7, "kb-a"),
	}}
	kb := &stubAuthorizedKnowledgeBaseService{bases: map[string]*types.KnowledgeBase{
		"kb-a": {ID: "kb-a", TenantID: 7},
		"kb-b": {ID: "kb-b", TenantID: 7},
	}}
	session := &stubAuthorizedSessionService{
		run: func(ctx context.Context, _ *types.QARequest, bus *event.EventBus) {
			bus.Emit(ctx, event.Event{Type: event.EventAgentFinalAnswer, Data: event.AgentFinalAnswerData{Content: "ok", Done: true}})
		},
	}
	engine := authorizedTestEngine(t, session, kb, ks)
	docs := []astaraAuthorizedDocument{
		{TenantID: "7", KnowledgeBaseID: "kb-a", KnowledgeID: "k-1", Revision: knownRevision},
		{TenantID: "7", KnowledgeBaseID: "kb-b", KnowledgeID: "k-2", Revision: knownRevision},
		{TenantID: "7", KnowledgeBaseID: "kb-a", KnowledgeID: "k-3", Revision: knownRevision},
	}
	body := makeValidAuthorizedBody(docs)
	recorder := authorizedRequest(t, engine, body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(session.calls) != 1 {
		t.Fatalf("calls=%d", len(session.calls))
	}
	req := session.calls[0]
	if len(req.KnowledgeIDs) != 3 {
		t.Fatalf("knowledge_ids=%v, want 3", req.KnowledgeIDs)
	}
	if len(req.KnowledgeBaseIDs) != 0 {
		t.Fatalf("knowledge_base_ids=%v, want nil/empty", req.KnowledgeBaseIDs)
	}
}

func TestAuthorizedAnswerPipelineErrorReturns502(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ks := &stubAuthorizedKnowledgeService{docs: map[string]*types.Knowledge{
		"k-1": makeKnowledge("k-1", 7, "kb-1"),
	}}
	kb := &stubAuthorizedKnowledgeBaseService{bases: map[string]*types.KnowledgeBase{
		"kb-1": {ID: "kb-1", TenantID: 7},
	}}
	session := &stubAuthorizedSessionService{
		run: func(ctx context.Context, _ *types.QARequest, bus *event.EventBus) {
			bus.Emit(ctx, event.Event{Type: event.EventError, Data: event.ErrorData{Error: "provider exploded"}})
		},
	}
	engine := authorizedTestEngine(t, session, kb, ks)
	docs := []astaraAuthorizedDocument{{TenantID: "7", KnowledgeBaseID: "kb-1", KnowledgeID: "k-1", Revision: knownRevision}}
	body := makeValidAuthorizedBody(docs)
	recorder := authorizedRequest(t, engine, body)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502", recorder.Code)
	}
}

func TestAuthorizedAnswerDetectsPostGenerationStateChange(t *testing.T) {
	for _, mutation := range []string{"revision", "updated", "deleted", "disabled", "parse", "tenant", "kb", "missing"} {
		t.Run(mutation, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			k1 := makeKnowledge("k-1", 7, "kb-1")
			ks := &stubAuthorizedKnowledgeService{docs: map[string]*types.Knowledge{
				"k-1": k1,
			}}
			kb := &stubAuthorizedKnowledgeBaseService{bases: map[string]*types.KnowledgeBase{
				"kb-1": {ID: "kb-1", TenantID: 7},
			}}
			session := &stubAuthorizedSessionService{
				run: func(ctx context.Context, _ *types.QARequest, bus *event.EventBus) {
					// Mutate the document during pipeline execution to simulate
					// a concurrent update.
					switch mutation {
					case "revision":
						k1.SourceRevision = 999
					case "updated":
						k1.UpdatedAt = k1.UpdatedAt.Add(time.Second)
					case "deleted":
						k1.DeletedAt.Valid = true
						k1.DeletedAt.Time = time.Now()
					case "disabled":
						k1.EnableStatus = "disabled"
					case "parse":
						k1.ParseStatus = "failed"
					case "tenant":
						k1.TenantID = 8
					case "kb":
						k1.KnowledgeBaseID = "changed-kb"
					case "missing":
						delete(ks.docs, "k-1")
					}
					bus.Emit(ctx, event.Event{Type: event.EventAgentFinalAnswer, Data: event.AgentFinalAnswerData{Content: "answer", Done: true}})
				},
			}
			engine := authorizedTestEngine(t, session, kb, ks)
			docs := []astaraAuthorizedDocument{{TenantID: "7", KnowledgeBaseID: "kb-1", KnowledgeID: "k-1", Revision: knownRevision}}
			body := makeValidAuthorizedBody(docs)
			recorder := authorizedRequest(t, engine, body)
			if recorder.Code != http.StatusConflict {
				t.Fatalf("status=%d, want 409 body=%s", recorder.Code, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), `"answer"`) {
				t.Fatal("answer without references escaped drift fence")
			}
		})
	}
}

func TestAuthorizedAnswerRejectsIncompleteOversizeAndForeignReferences(t *testing.T) {
	for _, mode := range []string{"incomplete", "oversize", "foreign", "error", "malformed-event", "malformed-references", "escaped-oversize"} {
		t.Run(mode, func(t *testing.T) {
			ks := &stubAuthorizedKnowledgeService{docs: map[string]*types.Knowledge{"k-1": makeKnowledge("k-1", 7, "kb-1")}}
			kb := &stubAuthorizedKnowledgeBaseService{bases: map[string]*types.KnowledgeBase{"kb-1": {ID: "kb-1", TenantID: 7}}}
			session := &stubAuthorizedSessionService{run: func(ctx context.Context, _ *types.QARequest, bus *event.EventBus) {
				content := "must-not-escape"
				if mode == "malformed-event" {
					bus.Emit(ctx, event.Event{Type: event.EventAgentReferences, Data: "invalid"})
				}
				if mode == "malformed-references" {
					bus.Emit(ctx, event.Event{Type: event.EventAgentReferences, Data: event.AgentReferencesData{References: "invalid"}})
				}
				if mode == "escaped-oversize" {
					content = strings.Repeat("\x00", 400000)
				}
				if mode == "oversize" {
					content = strings.Repeat("x", 1024*1024+1)
				}
				if mode == "foreign" {
					bus.Emit(ctx, event.Event{Type: event.EventAgentReferences, Data: event.AgentReferencesData{References: []*types.SearchResult{{KnowledgeID: "private", KnowledgeBaseID: "kb-1", Content: "private-reference"}}}})
				}
				if mode == "error" {
					bus.Emit(ctx, event.Event{Type: event.EventError, Data: event.ErrorData{Error: "password=secret"}})
				}
				if mode == "foreign" || mode == "error" {
					bus.Emit(ctx, event.Event{Type: event.EventError, Data: event.ErrorData{Error: ""}})
				}
				bus.Emit(ctx, event.Event{Type: event.EventAgentFinalAnswer, Data: event.AgentFinalAnswerData{Content: content, Done: mode != "incomplete"}})
			}}
			recorder := authorizedRequest(t, authorizedTestEngine(t, session, kb, ks), makeValidAuthorizedBody([]astaraAuthorizedDocument{{TenantID: "7", KnowledgeBaseID: "kb-1", KnowledgeID: "k-1", Revision: knownRevision}}))
			if recorder.Code != http.StatusBadGateway {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			for _, forbidden := range []string{"must-not-escape", "private-reference", "password=secret"} {
				if strings.Contains(recorder.Body.String(), forbidden) {
					t.Fatal("rejected content leaked")
				}
			}
		})
	}
}

func TestComputeAuthorizedDigestIsDeterministic(t *testing.T) {
	docs := []astaraAuthorizedDocument{
		{TenantID: "7", KnowledgeBaseID: "kb-2", KnowledgeID: "k-3", Revision: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
		{TenantID: "7", KnowledgeBaseID: "kb-1", KnowledgeID: "k-1", Revision: knownRevision},
		{TenantID: "7", KnowledgeBaseID: "kb-1", KnowledgeID: "k-2", Revision: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}
	d1 := computeAuthorizedDigest(docs)
	d2 := computeAuthorizedDigest(docs)
	if d1 != d2 {
		t.Fatalf("digest not deterministic: %s != %s", d1, d2)
	}
	// Different order should produce the same digest.
	reversed := []astaraAuthorizedDocument{docs[2], docs[1], docs[0]}
	d3 := computeAuthorizedDigest(reversed)
	if d1 != d3 {
		t.Fatalf("digest not order-independent: %s != %s", d1, d3)
	}
	// Different docs should produce a different digest.
	other := []astaraAuthorizedDocument{
		{TenantID: "7", KnowledgeBaseID: "kb-1", KnowledgeID: "k-1", Revision: "9999999999999999999999999999999999999999999999999999999999999999"},
	}
	d4 := computeAuthorizedDigest(other)
	if d1 == d4 {
		t.Fatalf("different docs produced same digest: %s", d1)
	}
}

func TestComputeAuthorizedDigestUnicodeGolden(t *testing.T) {
	docs := []astaraAuthorizedDocument{{TenantID: "42", KnowledgeBaseID: "kb-中文<&", KnowledgeID: "doc-\"\\\u2028\u2029\x01", Revision: strings.Repeat("a", 64)}}
	if got := computeAuthorizedDigest(docs); got != "fecfaeeafa8d5d28cba6123013d0410ff050e82581302dfd14b791abd42e36c7" {
		t.Fatalf("Unicode JSON digest mismatch: %s", got)
	}
}

func TestComputeAuthorizedDigestMatchesPythonTenantFirstOrdering(t *testing.T) {
	docs := []astaraAuthorizedDocument{
		{TenantID: "2", KnowledgeBaseID: "a", KnowledgeID: "one", Revision: strings.Repeat("a", 64)},
		{TenantID: "10", KnowledgeBaseID: "z", KnowledgeID: "two", Revision: strings.Repeat("b", 64)},
	}
	// Independently computed by Python json.dumps(sort_keys=True,
	// separators=(',', ':')), with tenant/KB/document lexical tuple ordering.
	const expected = "29a0a86ae18455a33da18e1036cdd63978735a71c7cc678a1cd6f168631a36ba"
	if got := computeAuthorizedDigest(docs); got != expected {
		t.Fatalf("Python contract mismatch: %s", got)
	}
}

// TestComputeAuthorizedDigestKnownFixture independently computes the expected
// digest for a fixed single-document fixture. The canonical JSON is:
//
//	[{"knowledge_base_id":"kb-1","knowledge_id":"doc-alpha","revision":"a1b2c3d4...","tenant_id":"42"}]
//
// The test verifies the canonical form matches exactly and the digest is a
// stable 64-char hex string.
func TestComputeAuthorizedDigestKnownFixture(t *testing.T) {
	docs := []astaraAuthorizedDocument{
		{TenantID: "42", KnowledgeBaseID: "kb-1", KnowledgeID: "doc-alpha", Revision: knownRevision},
	}
	got := computeAuthorizedDigest(docs)

	// The canonical JSON with sorted keys (knowledge_base_id < knowledge_id < revision < tenant_id).
	expectedCanonical := `[{"knowledge_base_id":"kb-1","knowledge_id":"doc-alpha","revision":"a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2","tenant_id":"42"}]`

	// Verify canonical form by recomputing digest from the expected string.
	h := sha256.Sum256([]byte(expectedCanonical))
	expectedDigest := fmt.Sprintf("%x", h)
	if got != expectedDigest {
		t.Fatalf("digest mismatch:\n got: %s\nwant: %s\ncanonical: %s", got, expectedDigest, expectedCanonical)
	}
	if len(got) != 64 {
		t.Fatalf("digest length=%d, want 64", len(got))
	}
	t.Logf("known fixture digest: %s", got)
	t.Logf("canonical JSON: %s", expectedCanonical)
}

func TestIsHexString(t *testing.T) {
	if !isHexString("0123456789abcdefABCDEF") {
		t.Fatal("valid hex rejected")
	}
	if isHexString("0123456789abcdefg") {
		t.Fatal("invalid hex accepted")
	}
	if isHexString("") {
		t.Fatal("empty string accepted")
	}
}

// Silence unused import warnings.
var _ = gorm.ErrRecordNotFound

func TestAuthorizedAnswerFailsClosedWithoutPlaneModelConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv(astaraServiceAuthEnv, "test-service-secret")
	ks := &stubAuthorizedKnowledgeService{docs: map[string]*types.Knowledge{
		"k-1": makeKnowledge("k-1", 7, "kb-1"),
	}}
	kb := &stubAuthorizedKnowledgeBaseService{bases: map[string]*types.KnowledgeBase{
		"kb-1": {ID: "kb-1", TenantID: 7},
	}}
	session := &stubAuthorizedSessionService{}
	repo := &stubAuthorizedModelRepo{rows: map[string]*types.Model{}}
	h := NewAstaraAnswerAuthorizedHandler(nil, session, kb, ks, repo)
	r := gin.New()
	r.Group("/api/v1/astara", AstaraServiceAuth).POST("/answer-authorized", h.AnswerAuthorized)
	docs := []astaraAuthorizedDocument{
		{TenantID: "7", KnowledgeBaseID: "kb-1", KnowledgeID: "k-1", Revision: knownRevision},
	}
	recorder := authorizedRequest(t, r, makeValidAuthorizedBody(docs))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s, want 503", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "knowledge_model_config_not_applied") {
		t.Fatalf("body=%s, want knowledge_model_config_not_applied", recorder.Body.String())
	}
	if len(session.calls) != 0 {
		t.Fatalf("KnowledgeQA called %d times without an applied model config", len(session.calls))
	}
}

func TestAuthorizedAnswerFailsClosedAfterRevocation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv(astaraServiceAuthEnv, "test-service-secret")
	ks := &stubAuthorizedKnowledgeService{docs: map[string]*types.Knowledge{
		"k-1": makeKnowledge("k-1", 7, "kb-1"),
	}}
	kb := &stubAuthorizedKnowledgeBaseService{bases: map[string]*types.KnowledgeBase{
		"kb-1": {ID: "kb-1", TenantID: 7},
	}}
	session := &stubAuthorizedSessionService{}
	revoked := appliedPlaneModel()
	revoked.DeletedAt = gorm.DeletedAt{Time: time.Now(), Valid: true}
	repo := &stubAuthorizedModelRepo{rows: map[string]*types.Model{
		types.PlaneOwnedKnowledgeQAModelID: revoked,
	}}
	h := NewAstaraAnswerAuthorizedHandler(nil, session, kb, ks, repo)
	r := gin.New()
	r.Group("/api/v1/astara", AstaraServiceAuth).POST("/answer-authorized", h.AnswerAuthorized)
	docs := []astaraAuthorizedDocument{
		{TenantID: "7", KnowledgeBaseID: "kb-1", KnowledgeID: "k-1", Revision: knownRevision},
	}
	recorder := authorizedRequest(t, r, makeValidAuthorizedBody(docs))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s, want 503", recorder.Code, recorder.Body.String())
	}
	if len(session.calls) != 0 {
		t.Fatalf("KnowledgeQA called %d times after revocation", len(session.calls))
	}
}

func TestAuthorizedAnswerBindsPlaneOwnedModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ks := &stubAuthorizedKnowledgeService{docs: map[string]*types.Knowledge{
		"k-1": makeKnowledge("k-1", 7, "kb-1"),
	}}
	kb := &stubAuthorizedKnowledgeBaseService{bases: map[string]*types.KnowledgeBase{
		"kb-1": {ID: "kb-1", TenantID: 7},
	}}
	session := &stubAuthorizedSessionService{
		run: func(ctx context.Context, _ *types.QARequest, bus *event.EventBus) {
			bus.Emit(ctx, event.Event{Type: event.EventAgentFinalAnswer, Data: event.AgentFinalAnswerData{Content: "ok", Done: true}})
		},
	}
	engine := authorizedTestEngine(t, session, kb, ks)
	docs := []astaraAuthorizedDocument{
		{TenantID: "7", KnowledgeBaseID: "kb-1", KnowledgeID: "k-1", Revision: knownRevision},
	}
	recorder := authorizedRequest(t, engine, makeValidAuthorizedBody(docs))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(session.calls) != 1 {
		t.Fatalf("KnowledgeQA calls=%d, want 1", len(session.calls))
	}
	if session.calls[0].SummaryModelID != types.PlaneOwnedKnowledgeQAModelID {
		t.Fatalf("SummaryModelID=%q, want the plane-owned KnowledgeQA row", session.calls[0].SummaryModelID)
	}
}
