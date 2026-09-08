// Copyright (c) 2026-present Astara and contributors
// SPDX-License-Identifier: Apache-2.0
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ── Stubs ──────────────────────────────────────────────────────────────────

// stubSearchSessionService embeds interfaces.SessionService and overrides
// SearchKnowledge to capture the call and simulate various outcomes.
// The optional run callback is invoked during SearchKnowledge to allow
// mutation of DB state during the search (for drift tests).
type stubSearchSessionService struct {
	interfaces.SessionService
	// captured tenant ID from context
	capturedTenantID uint64
	// captured knowledgeIDs from the call
	capturedDocIDs []string
	// captured query
	capturedQuery string
	// captured knowledgeBaseIDs
	capturedKBIDs []string
	// captured tagScopes
	capturedTagScopes []types.TagScope
	// results to return (configurable)
	results []*types.SearchResult
	// err to return (configurable)
	err error
	// run is an optional callback invoked during SearchKnowledge.
	// Used by drift tests to mutate DB mid-search.
	run func(ctx context.Context, knowledgeBaseIDs []string, knowledgeIDs []string, tagScopes []types.TagScope, query string)
}

func (s *stubSearchSessionService) SearchKnowledge(ctx context.Context, knowledgeBaseIDs []string, knowledgeIDs []string, tagScopes []types.TagScope, query string) ([]*types.SearchResult, error) {
	if tid, ok := ctx.Value(types.TenantIDContextKey).(uint64); ok {
		s.capturedTenantID = tid
	}
	s.capturedDocIDs = knowledgeIDs
	s.capturedQuery = query
	s.capturedKBIDs = knowledgeBaseIDs
	s.capturedTagScopes = tagScopes
	if s.run != nil {
		s.run(ctx, knowledgeBaseIDs, knowledgeIDs, tagScopes, query)
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.results, nil
}

// ── Helpers ────────────────────────────────────────────────────────────────

const searchTestRevision = "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"

func setupSearchTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:astara-search-" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&types.Tenant{}, &types.KnowledgeBase{}, &types.Knowledge{}, &types.Chunk{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func insertSearchTestData(t *testing.T, db *gorm.DB) (tenantID uint64, kbID, docID string) {
	t.Helper()
	tenant := &types.Tenant{Name: "T", Status: "active", Business: "astara"}
	if err := db.Create(tenant).Error; err != nil {
		t.Fatal(err)
	}
	tenantID = tenant.ID

	kbID = "kb-search-1"
	if err := db.Create(&types.KnowledgeBase{
		ID: kbID, Name: "Test KB", Type: types.KnowledgeBaseTypeDocument,
		TenantID: tenantID, EmbeddingModelID: "embed", SummaryModelID: "summary",
	}).Error; err != nil {
		t.Fatal(err)
	}

	docID = "doc-search-1"
	now := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	if err := db.Create(&types.Knowledge{
		ID: docID, TenantID: tenantID, KnowledgeBaseID: kbID,
		Title: "Test Document", Description: "A test document for search handler",
		FileName: "test.md", FileType: "markdown", FileSize: 1024,
		SourceRevision: 42, ParseStatus: types.ParseStatusCompleted,
		EnableStatus: "enabled", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	return tenantID, kbID, docID
}

func searchTestEngine(t *testing.T, db *gorm.DB, session *stubSearchSessionService) *gin.Engine {
	t.Helper()
	t.Setenv(astaraServiceAuthEnv, "test-service-secret")
	h := NewAstaraSearchAuthorizedHandler(db, session)
	r := gin.New()
	r.Group("/api/v1/astara", AstaraServiceAuth).POST("/search-authorized", h.SearchAuthorized)
	return r
}

func searchRequest(t *testing.T, engine *gin.Engine, body any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/astara/search-authorized", bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-service-secret")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	return recorder
}

// makeSearchDocs builds a single-doc astaraSearchDocument slice.
func makeSearchDocs(tenantID uint64, kbID, docID string) []astaraSearchDocument {
	return []astaraSearchDocument{{
		TenantID:        strconv.FormatUint(tenantID, 10),
		KnowledgeBaseID: kbID,
		KnowledgeID:     docID,
		Revision:        searchTestRevision,
	}}
}

// validSearchBody returns a valid request body with correct digest.
func validSearchBody(tenantID uint64, kbID, docID, query string) map[string]any {
	docs := makeSearchDocs(tenantID, kbID, docID)
	return map[string]any{
		"contract_version":     1,
		"query":                query,
		"documents":            docs,
		"authorization_digest": computeSearchDigest(docs),
	}
}

// makeSearchResult builds a *types.SearchResult for test use.
func makeSearchResult(kbID, docID, title, content string, score float64, chunkIndex int, id string) *types.SearchResult {
	return &types.SearchResult{
		KnowledgeBaseID: kbID,
		KnowledgeID:     docID,
		KnowledgeTitle:  title,
		Content:         content,
		Score:           score,
		ChunkIndex:      chunkIndex,
		ID:              id,
	}
}

// ── Digest golden tests ────────────────────────────────────────────────────

func TestComputeSearchDigestGoldenUnicode(t *testing.T) {
	// Golden: tenant 42, kb "kb-中文<&",
	// doc "doc-\"\u5c\u2028\u2029\x01", revision a*64.
	docID := "doc-\"\x5c\u2028\u2029\x01"
	revision := strings.Repeat("a", 64)
	docs := []astaraSearchDocument{{
		TenantID:        "42",
		KnowledgeBaseID: "kb-中文<&",
		KnowledgeID:     docID,
		Revision:        revision,
	}}
	got := computeSearchDigest(docs)
	const expected = "fecfaeeafa8d5d28cba6123013d0410ff050e82581302dfd14b791abd42e36c7"
	if got != expected {
		t.Fatalf("golden digest mismatch:\n got: %s\nwant: %s", got, expected)
	}
}

func TestComputeSearchDigestIsDeterministic(t *testing.T) {
	docs := []astaraSearchDocument{
		{TenantID: "7", KnowledgeBaseID: "kb-2", KnowledgeID: "k-3", Revision: strings.Repeat("c", 64)},
		{TenantID: "7", KnowledgeBaseID: "kb-1", KnowledgeID: "k-1", Revision: searchTestRevision},
		{TenantID: "7", KnowledgeBaseID: "kb-1", KnowledgeID: "k-2", Revision: strings.Repeat("b", 64)},
	}
	d1 := computeSearchDigest(docs)
	d2 := computeSearchDigest(docs)
	if d1 != d2 {
		t.Fatalf("digest not deterministic: %s != %s", d1, d2)
	}
	// Different order → same digest (sorted canonically).
	reversed := []astaraSearchDocument{docs[2], docs[1], docs[0]}
	d3 := computeSearchDigest(reversed)
	if d1 != d3 {
		t.Fatalf("digest not order-independent: %s != %s", d1, d3)
	}
}

func TestComputeSearchDigestMatchesPythonContract(t *testing.T) {
	docs := []astaraSearchDocument{
		{TenantID: "2", KnowledgeBaseID: "a", KnowledgeID: "one", Revision: strings.Repeat("a", 64)},
		{TenantID: "10", KnowledgeBaseID: "z", KnowledgeID: "two", Revision: strings.Repeat("b", 64)},
	}
	// Independently computed by Python:
	// json.dumps(sorted(docs, key=...), sort_keys=True, ensure_ascii=False, separators=(',',':'))
	const expected = "29a0a86ae18455a33da18e1036cdd63978735a71c7cc678a1cd6f168631a36ba"
	if got := computeSearchDigest(docs); got != expected {
		t.Fatalf("Python contract mismatch: %s", got)
	}
}

// ── Contract / wire shape ──────────────────────────────────────────────────

func TestSearchAuthorizedResponseNeverCacheable(t *testing.T) {
	db := setupSearchTestDB(t)
	tid, kb, doc := insertSearchTestData(t, db)
	session := &stubSearchSessionService{results: []*types.SearchResult{}}
	engine := searchTestEngine(t, db, session)
	for _, valid := range []bool{true, false} {
		body := validSearchBody(tid, kb, doc, "test query")
		if !valid {
			body["contract_version"] = 2
		}
		rec := searchRequest(t, engine, body)
		if rec.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("search response allows cache reuse")
		}
	}
}

func TestSearchAuthorizedSuccessShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupSearchTestDB(t)
	tid, kbID, docID := insertSearchTestData(t, db)
	session := &stubSearchSessionService{
		results: []*types.SearchResult{
			makeSearchResult(kbID, docID, "Test Document", "chunk content", 0.95, 0, "result-1"),
		},
	}
	engine := searchTestEngine(t, db, session)
	recorder := searchRequest(t, engine, validSearchBody(tid, kbID, docID, "test query"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	var resp astaraSearchAuthorizedResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.ContractVersion != 1 {
		t.Fatalf("contract_version=%d, want 1", resp.ContractVersion)
	}
	if resp.AuthorizationDigest == "" {
		t.Fatal("authorization_digest is empty")
	}
	if len(resp.Results) != 1 {
		t.Fatalf("results=%d, want 1", len(resp.Results))
	}
	r := resp.Results[0]
	if r.TenantID != strconv.FormatUint(tid, 10) {
		t.Fatalf("result.tenant_id=%q, want %d", r.TenantID, tid)
	}
	if r.KnowledgeBaseID != kbID {
		t.Fatalf("result.knowledge_base_id=%q", r.KnowledgeBaseID)
	}
	if r.KnowledgeID != docID {
		t.Fatalf("result.knowledge_id=%q", r.KnowledgeID)
	}
	if r.Content != "chunk content" {
		t.Fatalf("result.content=%q", r.Content)
	}
	if r.Score != 0.95 {
		t.Fatalf("result.score=%f", r.Score)
	}
	if r.ChunkIndex != 0 {
		t.Fatalf("result.chunk_index=%d", r.ChunkIndex)
	}
	if r.KnowledgeTitle != "Test Document" {
		t.Fatalf("result.knowledge_title=%q", r.KnowledgeTitle)
	}
	if r.ID != "result-1" {
		t.Fatalf("result.id=%q", r.ID)
	}

	// Verify exact SearchKnowledge call: nil KBIDs, explicit docIDs, nil tags.
	if session.capturedKBIDs != nil {
		t.Fatalf("KnowledgeBaseIDs=%v, want nil", session.capturedKBIDs)
	}
	if len(session.capturedDocIDs) != 1 || session.capturedDocIDs[0] != docID {
		t.Fatalf("KnowledgeIDs=%v, want [%s]", session.capturedDocIDs, docID)
	}
	if session.capturedTagScopes != nil {
		t.Fatalf("TagScopes=%v, want nil", session.capturedTagScopes)
	}
	if session.capturedQuery != "test query" {
		t.Fatalf("query=%q", session.capturedQuery)
	}
	if session.capturedTenantID != tid {
		t.Fatalf("tenantID=%d, want %d", session.capturedTenantID, tid)
	}

	// No extra fields in response.
	raw := recorder.Body.String()
	for _, forbidden := range []string{"file_path", "storage", "signed_url", "content_hash", "file_type"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("response leaked %q", forbidden)
		}
	}
}

// ── Strict validation ──────────────────────────────────────────────────────

func TestSearchAuthorizedRejectsWrongContractVersion(t *testing.T) {
	db := setupSearchTestDB(t)
	engine := searchTestEngine(t, db, &stubSearchSessionService{})
	body := map[string]any{"contract_version": 2, "query": "q",
		"documents": []map[string]any{{"tenant_id": "1", "knowledge_base_id": "kb", "knowledge_id": "d", "revision": searchTestRevision}}}
	if searchRequest(t, engine, body).Code != http.StatusBadRequest {
		t.Fatal("want 400")
	}
}

func TestSearchAuthorizedRejectsMissingContractVersion(t *testing.T) {
	db := setupSearchTestDB(t)
	engine := searchTestEngine(t, db, &stubSearchSessionService{})
	body := map[string]any{"query": "q", "documents": []map[string]any{{"tenant_id": "1"}}}
	if searchRequest(t, engine, body).Code != http.StatusBadRequest {
		t.Fatal("want 400")
	}
}

func TestSearchAuthorizedRejectsEmptyQuery(t *testing.T) {
	db := setupSearchTestDB(t)
	engine := searchTestEngine(t, db, &stubSearchSessionService{})
	body := map[string]any{"contract_version": 1, "query": "", "documents": []map[string]any{{"tenant_id": "1"}}}
	if searchRequest(t, engine, body).Code != http.StatusBadRequest {
		t.Fatal("want 400")
	}
}

func TestSearchAuthorizedRejectsQueryOver2000Runes(t *testing.T) {
	db := setupSearchTestDB(t)
	engine := searchTestEngine(t, db, &stubSearchSessionService{})
	body := map[string]any{"contract_version": 1, "query": strings.Repeat("中", 2001),
		"documents": []map[string]any{{"tenant_id": "1"}}}
	if searchRequest(t, engine, body).Code != http.StatusBadRequest {
		t.Fatal("want 400")
	}
}

func TestSearchAuthorizedAcceptsQueryAt2000Runes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupSearchTestDB(t)
	tid, kbID, docID := insertSearchTestData(t, db)
	query := strings.Repeat("中", 2000)
	session := &stubSearchSessionService{results: []*types.SearchResult{}}
	engine := searchTestEngine(t, db, session)
	body := validSearchBody(tid, kbID, docID, query)
	if searchRequest(t, engine, body).Code != http.StatusOK {
		t.Fatal("want 200 for exactly 2000 runes")
	}
}

func TestSearchAuthorizedRejectsEmptyDocuments(t *testing.T) {
	db := setupSearchTestDB(t)
	engine := searchTestEngine(t, db, &stubSearchSessionService{})
	body := map[string]any{"contract_version": 1, "query": "q", "documents": []map[string]any{}}
	if searchRequest(t, engine, body).Code != http.StatusBadRequest {
		t.Fatal("want 400")
	}
}

func TestSearchAuthorizedRejectsUnknownFields(t *testing.T) {
	db := setupSearchTestDB(t)
	tid, kbID, docID := insertSearchTestData(t, db)
	engine := searchTestEngine(t, db, &stubSearchSessionService{})
	body := validSearchBody(tid, kbID, docID, "q")
	body["extra"] = true
	if searchRequest(t, engine, body).Code != http.StatusBadRequest {
		t.Fatal("want 400 for unknown field")
	}
}

func TestSearchAuthorizedRejectsTrailingAndOversizeJSON(t *testing.T) {
	db := setupSearchTestDB(t)
	engine := searchTestEngine(t, db, &stubSearchSessionService{})
	// Trailing JSON
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/astara/search-authorized",
		bytes.NewReader([]byte(`{"contract_version":1,"query":"q","documents":[]} {"extra":true}`)))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("Authorization", "Bearer test-service-secret")
	if (func() int { r := httptest.NewRecorder(); engine.ServeHTTP(r, req1); return r.Code })() != http.StatusBadRequest {
		t.Fatal("trailing: want 400")
	}
	// Oversize body
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/astara/search-authorized",
		bytes.NewReader([]byte(strings.Repeat(" ", 512*1024+1)+`{}`)))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "Bearer test-service-secret")
	if (func() int { r := httptest.NewRecorder(); engine.ServeHTTP(r, req2); return r.Code })() != http.StatusBadRequest {
		t.Fatal("oversize: want 400")
	}
}

// ── Document field validation ──────────────────────────────────────────────

func TestSearchAuthorizedRejectsSurroundingWhitespace(t *testing.T) {
	db := setupSearchTestDB(t)
	tid, kbID, docID := insertSearchTestData(t, db)
	engine := searchTestEngine(t, db, &stubSearchSessionService{})
	for _, field := range []string{"tenant_id", "knowledge_base_id", "knowledge_id", "revision"} {
		docs := makeSearchDocs(tid, kbID, docID)
		body := map[string]any{
			"contract_version":     1,
			"query":                "q",
			"documents":            docs,
			"authorization_digest": computeSearchDigest(docs),
		}
		// Inject whitespace into the document field via raw JSON
		rawJSON, _ := json.Marshal(body)
		var raw map[string]json.RawMessage
		json.Unmarshal(rawJSON, &raw)
		var rawDocs []map[string]json.RawMessage
		json.Unmarshal(raw["documents"], &rawDocs)
		fieldVal := string(rawDocs[0][field])
		// fieldVal is a JSON string like "\"value\""; inject space inside quotes
		rawDocs[0][field] = json.RawMessage(`" ` + strings.Trim(fieldVal, `"`) + `"`)
		rawDocsBytes, _ := json.Marshal(rawDocs)
		raw["documents"] = rawDocsBytes
		final, _ := json.Marshal(raw)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/astara/search-authorized", bytes.NewReader(final))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer test-service-secret")
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("whitespace %s: want 400 got %d body=%s", field, rec.Code, rec.Body.String())
		}
	}
}

func TestSearchAuthorizedRejectsNonCanonicalTenantID(t *testing.T) {
	db := setupSearchTestDB(t)
	_, kbID, docID := insertSearchTestData(t, db)
	engine := searchTestEngine(t, db, &stubSearchSessionService{})
	for _, bad := range []string{"07", "007", "+7"} {
		docs := []astaraSearchDocument{{TenantID: bad, KnowledgeBaseID: kbID, KnowledgeID: docID, Revision: searchTestRevision}}
		body := map[string]any{"contract_version": 1, "query": "q", "documents": docs, "authorization_digest": computeSearchDigest(docs)}
		if searchRequest(t, engine, body).Code != http.StatusBadRequest {
			t.Fatalf("tenant_id=%q: want 400", bad)
		}
	}
}

func TestSearchAuthorizedRejectsNonNumericTenantID(t *testing.T) {
	db := setupSearchTestDB(t)
	engine := searchTestEngine(t, db, &stubSearchSessionService{})
	docs := []astaraSearchDocument{{TenantID: "not-a-number", KnowledgeBaseID: "kb", KnowledgeID: "d", Revision: searchTestRevision}}
	body := map[string]any{"contract_version": 1, "query": "q", "documents": docs, "authorization_digest": computeSearchDigest(docs)}
	if searchRequest(t, engine, body).Code != http.StatusBadRequest {
		t.Fatal("want 400")
	}
}

func TestSearchAuthorizedRejectsMalformedRevision(t *testing.T) {
	db := setupSearchTestDB(t)
	tid, kbID, docID := insertSearchTestData(t, db)
	engine := searchTestEngine(t, db, &stubSearchSessionService{})
	for _, rev := range []string{
		"abcd1234",                    // too short
		strings.Repeat("z", 64),       // not hex
		strings.Repeat("a", 63),       // wrong length
		strings.Repeat("A", 64),       // uppercase hex
		strings.Repeat("a", 64) + " ", // trailing space
	} {
		docs := []astaraSearchDocument{{TenantID: strconv.FormatUint(tid, 10), KnowledgeBaseID: kbID, KnowledgeID: docID, Revision: rev}}
		body := map[string]any{"contract_version": 1, "query": "q", "documents": docs, "authorization_digest": computeSearchDigest(docs)}
		if searchRequest(t, engine, body).Code != http.StatusBadRequest {
			t.Fatalf("revision=%q: want 400", rev)
		}
	}
}

func TestSearchAuthorizedRejectsDuplicateKnowledgeIDs(t *testing.T) {
	db := setupSearchTestDB(t)
	tid, kbID, _ := insertSearchTestData(t, db)
	engine := searchTestEngine(t, db, &stubSearchSessionService{})
	docs := []astaraSearchDocument{
		{TenantID: strconv.FormatUint(tid, 10), KnowledgeBaseID: kbID, KnowledgeID: "dup", Revision: searchTestRevision},
		{TenantID: strconv.FormatUint(tid, 10), KnowledgeBaseID: kbID, KnowledgeID: "dup", Revision: searchTestRevision},
	}
	body := map[string]any{"contract_version": 1, "query": "q", "documents": docs, "authorization_digest": computeSearchDigest(docs)}
	if searchRequest(t, engine, body).Code != http.StatusBadRequest {
		t.Fatal("want 400 for duplicate knowledge_id")
	}
}

func TestSearchAuthorizedRejectsDigestMismatch(t *testing.T) {
	db := setupSearchTestDB(t)
	tid, kbID, docID := insertSearchTestData(t, db)
	engine := searchTestEngine(t, db, &stubSearchSessionService{})
	docs := makeSearchDocs(tid, kbID, docID)
	body := map[string]any{
		"contract_version":     1,
		"query":                "q",
		"documents":            docs,
		"authorization_digest": "0000000000000000000000000000000000000000000000000000000000000000",
	}
	if searchRequest(t, engine, body).Code != http.StatusBadRequest {
		t.Fatal("want 400 for digest mismatch")
	}
}

// ── Not found / wrong scope ────────────────────────────────────────────────

func TestSearchAuthorizedRejectsWrongTenant(t *testing.T) {
	db := setupSearchTestDB(t)
	_, kbID, docID := insertSearchTestData(t, db)
	engine := searchTestEngine(t, db, &stubSearchSessionService{})
	docs := []astaraSearchDocument{{TenantID: "99999", KnowledgeBaseID: kbID, KnowledgeID: docID, Revision: searchTestRevision}}
	body := map[string]any{"contract_version": 1, "query": "q", "documents": docs, "authorization_digest": computeSearchDigest(docs)}
	if searchRequest(t, engine, body).Code != http.StatusNotFound {
		t.Fatal("want 404")
	}
}

func TestSearchAuthorizedRejectsWrongKB(t *testing.T) {
	db := setupSearchTestDB(t)
	tid, _, docID := insertSearchTestData(t, db)
	engine := searchTestEngine(t, db, &stubSearchSessionService{})
	docs := []astaraSearchDocument{{TenantID: strconv.FormatUint(tid, 10), KnowledgeBaseID: "kb-nonexistent", KnowledgeID: docID, Revision: searchTestRevision}}
	body := map[string]any{"contract_version": 1, "query": "q", "documents": docs, "authorization_digest": computeSearchDigest(docs)}
	if searchRequest(t, engine, body).Code != http.StatusNotFound {
		t.Fatal("want 404")
	}
}

func TestSearchAuthorizedRejectsWrongDoc(t *testing.T) {
	db := setupSearchTestDB(t)
	tid, kbID, _ := insertSearchTestData(t, db)
	engine := searchTestEngine(t, db, &stubSearchSessionService{})
	docs := []astaraSearchDocument{{TenantID: strconv.FormatUint(tid, 10), KnowledgeBaseID: kbID, KnowledgeID: "doc-nonexistent", Revision: searchTestRevision}}
	body := map[string]any{"contract_version": 1, "query": "q", "documents": docs, "authorization_digest": computeSearchDigest(docs)}
	if searchRequest(t, engine, body).Code != http.StatusNotFound {
		t.Fatal("want 404")
	}
}

func TestSearchAuthorizedRejectsDeletedDoc(t *testing.T) {
	db := setupSearchTestDB(t)
	tid, kbID, docID := insertSearchTestData(t, db)
	engine := searchTestEngine(t, db, &stubSearchSessionService{})
	db.Model(&types.Knowledge{}).Where("id = ?", docID).Update("deleted_at", time.Now())
	docs := makeSearchDocs(tid, kbID, docID)
	body := map[string]any{"contract_version": 1, "query": "q", "documents": docs, "authorization_digest": computeSearchDigest(docs)}
	if searchRequest(t, engine, body).Code != http.StatusNotFound {
		t.Fatal("want 404 for deleted doc")
	}
}

func TestSearchAuthorizedRejectsDeletedKB(t *testing.T) {
	db := setupSearchTestDB(t)
	tid, kbID, docID := insertSearchTestData(t, db)
	engine := searchTestEngine(t, db, &stubSearchSessionService{})
	db.Model(&types.KnowledgeBase{}).Where("id = ?", kbID).Update("deleted_at", time.Now())
	docs := makeSearchDocs(tid, kbID, docID)
	body := map[string]any{"contract_version": 1, "query": "q", "documents": docs, "authorization_digest": computeSearchDigest(docs)}
	if searchRequest(t, engine, body).Code != http.StatusNotFound {
		t.Fatal("want 404 for deleted KB")
	}
}

func TestSearchAuthorizedRejectsNonQueryableDoc(t *testing.T) {
	db := setupSearchTestDB(t)
	tid, kbID, docID := insertSearchTestData(t, db)
	engine := searchTestEngine(t, db, &stubSearchSessionService{})
	for _, state := range []string{"pending", "processing", "failed", "deleting"} {
		db.Model(&types.Knowledge{}).Where("id = ?", docID).Update("parse_status", state)
		docs := makeSearchDocs(tid, kbID, docID)
		body := map[string]any{"contract_version": 1, "query": "q", "documents": docs, "authorization_digest": computeSearchDigest(docs)}
		if searchRequest(t, engine, body).Code != http.StatusConflict {
			t.Fatalf("parse_status=%s: want 409", state)
		}
	}
	db.Model(&types.Knowledge{}).Where("id = ?", docID).Updates(map[string]any{
		"parse_status": types.ParseStatusCompleted, "enable_status": "disabled",
	})
	docs := makeSearchDocs(tid, kbID, docID)
	body := map[string]any{"contract_version": 1, "query": "q", "documents": docs, "authorization_digest": computeSearchDigest(docs)}
	if searchRequest(t, engine, body).Code != http.StatusConflict {
		t.Fatal("disabled: want 409")
	}
}

func TestSearchAuthorizedRejectsInactiveTenant(t *testing.T) {
	db := setupSearchTestDB(t)
	tid, kbID, docID := insertSearchTestData(t, db)
	engine := searchTestEngine(t, db, &stubSearchSessionService{})
	db.Model(&types.Tenant{}).Where("id = ?", tid).Update("status", "suspended")
	docs := makeSearchDocs(tid, kbID, docID)
	body := map[string]any{"contract_version": 1, "query": "q", "documents": docs, "authorization_digest": computeSearchDigest(docs)}
	if searchRequest(t, engine, body).Code != http.StatusForbidden {
		t.Fatal("want 403 for inactive tenant")
	}
}

func TestSearchAuthorizedRejectsMixedTenantDocuments(t *testing.T) {
	db := setupSearchTestDB(t)
	tid, kbID, docID := insertSearchTestData(t, db)
	// Create second tenant
	t2 := &types.Tenant{Name: "T2", Status: "active", Business: "astara"}
	db.Create(t2)
	engine := searchTestEngine(t, db, &stubSearchSessionService{})
	docs := []astaraSearchDocument{
		{TenantID: strconv.FormatUint(tid, 10), KnowledgeBaseID: kbID, KnowledgeID: docID, Revision: searchTestRevision},
		{TenantID: strconv.FormatUint(t2.ID, 10), KnowledgeBaseID: kbID, KnowledgeID: docID, Revision: searchTestRevision},
	}
	body := map[string]any{"contract_version": 1, "query": "q", "documents": docs, "authorization_digest": computeSearchDigest(docs)}
	if searchRequest(t, engine, body).Code != http.StatusBadRequest {
		t.Fatal("want 400 for mixed tenant")
	}
}

// ── Result scope validation ────────────────────────────────────────────────

func TestSearchAuthorizedRejectsOmittedDocResult(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupSearchTestDB(t)
	tid, kbID, docID := insertSearchTestData(t, db)
	// Search returns a doc not in the admitted scope.
	session := &stubSearchSessionService{
		results: []*types.SearchResult{
			makeSearchResult(kbID, "foreign-doc", "Leaked", "secret", 0.9, 0, "r-1"),
		},
	}
	engine := searchTestEngine(t, db, session)
	body := validSearchBody(tid, kbID, docID, "q")
	if searchRequest(t, engine, body).Code != http.StatusBadGateway {
		t.Fatal("want 502 for omitted doc result")
	}
}

func TestSearchAuthorizedRejectsMismatchedKBResult(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupSearchTestDB(t)
	tid, kbID, docID := insertSearchTestData(t, db)
	session := &stubSearchSessionService{
		results: []*types.SearchResult{
			makeSearchResult("wrong-kb", docID, "Title", "content", 0.9, 0, "r-1"),
		},
	}
	engine := searchTestEngine(t, db, session)
	body := validSearchBody(tid, kbID, docID, "q")
	if searchRequest(t, engine, body).Code != http.StatusBadGateway {
		t.Fatal("want 502 for KB mismatch")
	}
}

func TestSearchAuthorizedRejectsDuplicateResultIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupSearchTestDB(t)
	tid, kbID, docID := insertSearchTestData(t, db)
	session := &stubSearchSessionService{
		results: []*types.SearchResult{
			makeSearchResult(kbID, docID, "T", "c1", 0.9, 0, "dup-id"),
			makeSearchResult(kbID, docID, "T", "c2", 0.8, 1, "dup-id"),
		},
	}
	engine := searchTestEngine(t, db, session)
	body := validSearchBody(tid, kbID, docID, "q")
	if searchRequest(t, engine, body).Code != http.StatusBadGateway {
		t.Fatal("want 502 for duplicate result IDs")
	}
}

func TestSearchAuthorizedRejectsNonFiniteScore(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupSearchTestDB(t)
	tid, kbID, docID := insertSearchTestData(t, db)
	for _, score := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		session := &stubSearchSessionService{
			results: []*types.SearchResult{
				makeSearchResult(kbID, docID, "T", "c", score, 0, "r-1"),
			},
		}
		engine := searchTestEngine(t, db, session)
		body := validSearchBody(tid, kbID, docID, "q")
		if searchRequest(t, engine, body).Code != http.StatusBadGateway {
			t.Fatalf("score=%v: want 502", score)
		}
	}
}

func TestSearchAuthorizedRejectsNegativeChunkIndex(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupSearchTestDB(t)
	tid, kbID, docID := insertSearchTestData(t, db)
	session := &stubSearchSessionService{
		results: []*types.SearchResult{
			makeSearchResult(kbID, docID, "T", "c", 0.9, -1, "r-1"),
		},
	}
	engine := searchTestEngine(t, db, session)
	body := validSearchBody(tid, kbID, docID, "q")
	if searchRequest(t, engine, body).Code != http.StatusBadGateway {
		t.Fatal("want 502 for negative chunk_index")
	}
}

func TestSearchAuthorizedRejectsEmptyResultID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupSearchTestDB(t)
	tid, kbID, docID := insertSearchTestData(t, db)
	session := &stubSearchSessionService{
		results: []*types.SearchResult{
			makeSearchResult(kbID, docID, "T", "c", 0.9, 0, ""),
		},
	}
	engine := searchTestEngine(t, db, session)
	body := validSearchBody(tid, kbID, docID, "q")
	if searchRequest(t, engine, body).Code != http.StatusBadGateway {
		t.Fatal("want 502 for empty result ID")
	}
}

// ── Post-search revalidation (drift) ───────────────────────────────────────

func TestSearchAuthorizedDetectsTenantDisabledDuringSearch(t *testing.T) {
	testSearchDrift(t, "tenant-disabled", func(db *gorm.DB, tid uint64, kbID, docID string) {
		db.Model(&types.Tenant{}).Where("id = ?", tid).Update("status", "suspended")
	})
}

func TestSearchAuthorizedDetectsKBDeletedDuringSearch(t *testing.T) {
	testSearchDrift(t, "kb-deleted", func(db *gorm.DB, tid uint64, kbID, docID string) {
		db.Model(&types.KnowledgeBase{}).Where("id = ?", kbID).Update("deleted_at", time.Now())
	})
}

func TestSearchAuthorizedDetectsKBTenantChangedDuringSearch(t *testing.T) {
	testSearchDrift(t, "kb-tenant", func(db *gorm.DB, tid uint64, kbID, docID string) {
		newT := &types.Tenant{Name: "Moved", Status: "active", Business: "astara"}
		db.Create(newT)
		db.Model(&types.KnowledgeBase{}).Where("id = ?", kbID).Update("tenant_id", newT.ID)
	})
}

func TestSearchAuthorizedDetectsDocDeletedDuringSearch(t *testing.T) {
	testSearchDrift(t, "doc-deleted", func(db *gorm.DB, tid uint64, kbID, docID string) {
		db.Model(&types.Knowledge{}).Where("id = ?", docID).Update("deleted_at", time.Now())
	})
}

func TestSearchAuthorizedDetectsDocDisabledDuringSearch(t *testing.T) {
	testSearchDrift(t, "doc-disabled", func(db *gorm.DB, tid uint64, kbID, docID string) {
		db.Model(&types.Knowledge{}).Where("id = ?", docID).Update("enable_status", "disabled")
	})
}

func TestSearchAuthorizedDetectsDocParseFailedDuringSearch(t *testing.T) {
	testSearchDrift(t, "doc-parse-failed", func(db *gorm.DB, tid uint64, kbID, docID string) {
		db.Model(&types.Knowledge{}).Where("id = ?", docID).Update("parse_status", "failed")
	})
}

func TestSearchAuthorizedDetectsDocKBChangedDuringSearch(t *testing.T) {
	testSearchDrift(t, "doc-kb-changed", func(db *gorm.DB, tid uint64, kbID, docID string) {
		db.Model(&types.Knowledge{}).Where("id = ?", docID).Update("knowledge_base_id", "kb-other")
	})
}

func TestSearchAuthorizedDetectsDocRevisionChangedDuringSearch(t *testing.T) {
	testSearchDrift(t, "doc-revision", func(db *gorm.DB, tid uint64, kbID, docID string) {
		db.Model(&types.Knowledge{}).Where("id = ?", docID).Update("source_revision", 999)
	})
}

func TestSearchAuthorizedDetectsDocUpdatedChangedDuringSearch(t *testing.T) {
	testSearchDrift(t, "doc-updated", func(db *gorm.DB, tid uint64, kbID, docID string) {
		db.Model(&types.Knowledge{}).Where("id = ?", docID).Update("updated_at", time.Now().Add(time.Hour))
	})
}

func TestSearchAuthorizedDetectsDocTenantChangedDuringSearch(t *testing.T) {
	testSearchDrift(t, "doc-tenant", func(db *gorm.DB, tid uint64, kbID, docID string) {
		newT := &types.Tenant{Name: "DocMoved", Status: "active", Business: "astara"}
		db.Create(newT)
		db.Model(&types.Knowledge{}).Where("id = ?", docID).Update("tenant_id", newT.ID)
	})
}

// testSearchDrift inserts baseline data, then uses the stub's run callback
// (which fires during SearchKnowledge) to apply the mutation. The handler's
// post-search revalidation queries then see the drift and return 409 with
// no data leak.
func testSearchDrift(t *testing.T, name string, mutate func(*gorm.DB, uint64, string, string)) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dsn := "file:astara-search-drift-" + name + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&types.Tenant{}, &types.KnowledgeBase{}, &types.Knowledge{}, &types.Chunk{}); err != nil {
		t.Fatal(err)
	}
	tid, kbID, docID := insertSearchTestData(t, db)

	session := &stubSearchSessionService{
		results: []*types.SearchResult{},
		run: func(_ context.Context, _ []string, _ []string, _ []types.TagScope, _ string) {
			mutate(db, tid, kbID, docID)
		},
	}

	engine := searchTestEngine(t, db, session)
	docs := makeSearchDocs(tid, kbID, docID)
	body := map[string]any{
		"contract_version":     1,
		"query":                "q",
		"documents":            docs,
		"authorization_digest": computeSearchDigest(docs),
	}
	recorder := searchRequest(t, engine, body)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("mutation=%s status=%d body=%s, want 409", name, recorder.Code, recorder.Body.String())
	}
	resp := recorder.Body.String()
	if strings.Contains(resp, `"results"`) {
		t.Fatalf("mutation=%s: data leaked on conflict: %s", name, resp)
	}
}

// ── Bounds / error isolation ───────────────────────────────────────────────

func TestSearchAuthorizedEncodedResponseBound(t *testing.T) {
	for _, content := range []string{strings.Repeat("x", 2*1024*1024+1), strings.Repeat("<", 1024*1024)} {
		t.Run(fmt.Sprint(len(content)), func(t *testing.T) {
			db := setupSearchTestDB(t)
			tid, kb, doc := insertSearchTestData(t, db)
			session := &stubSearchSessionService{results: []*types.SearchResult{makeSearchResult(kb, doc, "title", content, 0.5, 0, "chunk")}}
			rec := searchRequest(t, searchTestEngine(t, db, session), validSearchBody(tid, kb, doc, "query"))
			if rec.Code != http.StatusBadGateway || rec.Body.Len() > 256 {
				t.Fatalf("oversize escaped: code=%d bytes=%d", rec.Code, rec.Body.Len())
			}
		})
	}
}

func TestSearchAuthorizedTooManyResults(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupSearchTestDB(t)
	tid, kbID, docID := insertSearchTestData(t, db)
	// Build 1001 results.
	results := make([]*types.SearchResult, 1001)
	for i := range results {
		results[i] = makeSearchResult(kbID, docID, "T", "c", 0.5, i, fmt.Sprintf("r-%d", i))
	}
	session := &stubSearchSessionService{results: results}
	engine := searchTestEngine(t, db, session)
	body := validSearchBody(tid, kbID, docID, "q")
	if searchRequest(t, engine, body).Code != http.StatusBadGateway {
		t.Fatal("want 502 for >1000 results")
	}
}

func TestSearchAuthorizedSearchErrorReturns500(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupSearchTestDB(t)
	tid, kbID, docID := insertSearchTestData(t, db)
	session := &stubSearchSessionService{err: fmt.Errorf("backend exploded")}
	engine := searchTestEngine(t, db, session)
	body := validSearchBody(tid, kbID, docID, "q")
	if searchRequest(t, engine, body).Code != http.StatusInternalServerError {
		t.Fatal("want 500")
	}
}

func TestSearchAuthorizedNoStorageLeakage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupSearchTestDB(t)
	tid, kbID, docID := insertSearchTestData(t, db)
	db.Model(&types.Knowledge{}).Where("id = ?", docID).Update("file_path", "local://tenant/secret-file.pdf")
	session := &stubSearchSessionService{results: []*types.SearchResult{}}
	engine := searchTestEngine(t, db, session)
	body := validSearchBody(tid, kbID, docID, "q")
	recorder := searchRequest(t, engine, body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d", recorder.Code)
	}
	resp := recorder.Body.String()
	for _, forbidden := range []string{"file_path", "secret-file", "storage", "signed_url", "content_hash"} {
		if strings.Contains(resp, forbidden) {
			t.Fatalf("response leaked %q", forbidden)
		}
	}
}

func TestSearchAuthorizedRequiresServiceAuth(t *testing.T) {
	db := setupSearchTestDB(t)
	engine := searchTestEngine(t, db, &stubSearchSessionService{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/astara/search-authorized",
		bytes.NewReader([]byte(`{"contract_version":1,"query":"q","documents":[]}`)))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", recorder.Code)
	}
}

func TestSearchAuthorizedResponseOmitsInternalFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupSearchTestDB(t)
	tid, kbID, docID := insertSearchTestData(t, db)
	session := &stubSearchSessionService{
		results: []*types.SearchResult{
			makeSearchResult(kbID, docID, "Title", "content", 0.9, 0, "r-1"),
		},
	}
	engine := searchTestEngine(t, db, session)
	body := validSearchBody(tid, kbID, docID, "q")
	recorder := searchRequest(t, engine, body)
	resp := recorder.Body.String()
	// Response should have exactly: contract_version, authorization_digest, results
	for _, forbidden := range []string{"file_type", "file_size", "description", "source", "metadata", "parse_status"} {
		if strings.Contains(resp, forbidden) {
			t.Fatalf("response leaked internal field %q", forbidden)
		}
	}
}
