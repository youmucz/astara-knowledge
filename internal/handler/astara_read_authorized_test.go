// Copyright (c) 2026-present Astara and contributors
// SPDX-License-Identifier: Apache-2.0
package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
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
)

// ── Test helpers ──────────────────────────────────────────────────────────

// readTestRevision is a fixed lowercase 64-char hex string.
const readTestRevision = "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"

func setupReadTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:astara-read-" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&types.Tenant{}, &types.KnowledgeBase{}, &types.Knowledge{}, &types.Chunk{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func insertReadTestData(t *testing.T, db *gorm.DB) (tenantID uint64, kbID, docID string) {
	t.Helper()
	tenant := &types.Tenant{Name: "T", Status: "active", Business: "astara"}
	if err := db.Create(tenant).Error; err != nil {
		t.Fatal(err)
	}
	tenantID = tenant.ID

	kbID = "kb-read-1"
	if err := db.Create(&types.KnowledgeBase{
		ID: kbID, Name: "Test KB", Type: types.KnowledgeBaseTypeDocument,
		TenantID: tenantID, EmbeddingModelID: "embed", SummaryModelID: "summary",
	}).Error; err != nil {
		t.Fatal(err)
	}

	docID = "doc-read-1"
	now := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	if err := db.Create(&types.Knowledge{
		ID: docID, TenantID: tenantID, KnowledgeBaseID: kbID,
		Title: "Test Document", Description: "A test document for read handler",
		FileName: "test.md", FileType: "markdown", FileSize: 1024,
		SourceRevision: 42, ParseStatus: types.ParseStatusCompleted,
		EnableStatus: "enabled", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	for i, content := range []string{"chunk-A", "chunk-B", "chunk-C"} {
		if err := db.Create(&types.Chunk{
			ID: "ch-" + strconv.Itoa(i), TenantID: tenantID,
			KnowledgeBaseID: kbID, KnowledgeID: docID,
			Content: content, ChunkIndex: i,
			ChunkType: types.ChunkTypeText, IsEnabled: true,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return tenantID, kbID, docID
}

func readTestEngine(t *testing.T, db *gorm.DB) *gin.Engine {
	t.Helper()
	t.Setenv(astaraServiceAuthEnv, "test-service-secret")
	h := NewAstaraReadAuthorizedHandler(db)
	r := gin.New()
	r.Group("/api/v1/astara", AstaraServiceAuth).POST("/read-authorized", h.ReadAuthorized)
	return r
}

func readRequest(t *testing.T, engine *gin.Engine, body any) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/astara/read-authorized", bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-service-secret")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	return recorder
}

func validReadBody(tenantID uint64, kbID, docID string) map[string]any {
	return map[string]any{
		"contract_version": 1,
		"document": map[string]any{
			"tenant_id":         strconv.FormatUint(tenantID, 10),
			"knowledge_base_id": kbID,
			"knowledge_id":      docID,
			"revision":          readTestRevision,
		},
		"page":      1,
		"page_size": 20,
	}
}

func TestReadAuthorizedRejectsOversizeEncodedResponse(t *testing.T) {
	for _, content := range []string{strings.Repeat("x", astaraReadMaxResponseBytes+1), strings.Repeat("<", astaraReadMaxResponseBytes/2)} {
		t.Run(fmt.Sprint(len(content)), func(t *testing.T) {
			db := setupReadTestDB(t)
			tenant, kb, doc := insertReadTestData(t, db)
			if err := db.Model(&types.Chunk{}).Where("knowledge_id = ?", doc).Update("content", content).Error; err != nil {
				t.Fatal(err)
			}
			rec := readRequest(t, readTestEngine(t, db), validReadBody(tenant, kb, doc))
			if rec.Code != http.StatusBadGateway || rec.Body.Len() > 256 {
				t.Fatalf("oversize response escaped: status=%d size=%d", rec.Code, rec.Body.Len())
			}
		})
	}
}

// ── Contract / wire shape ────────────────────────────────────────────────

func TestReadAuthorizedResponseNeverCacheable(t *testing.T) {
	db := setupReadTestDB(t)
	tid, kb, doc := insertReadTestData(t, db)
	engine := readTestEngine(t, db)
	for _, valid := range []bool{true, false} {
		body := validReadBody(tid, kb, doc)
		if !valid {
			body["page"] = 0
		}
		rec := readRequest(t, engine, body)
		if rec.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("scoped read response allows cache reuse")
		}
		if valid && rec.Code != 200 {
			t.Fatalf("valid read failed: %d", rec.Code)
		}
		if !valid && rec.Code != 400 {
			t.Fatalf("invalid read accepted: %d", rec.Code)
		}
	}
}

func TestReadAuthorizedSuccessShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupReadTestDB(t)
	tid, kbID, docID := insertReadTestData(t, db)
	engine := readTestEngine(t, db)

	recorder := readRequest(t, engine, validReadBody(tid, kbID, docID))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	var resp astaraReadAuthorizedResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Contract version.
	if resp.ContractVersion != 1 {
		t.Fatalf("contract_version=%d, want 1", resp.ContractVersion)
	}

	// Outer document echoes request strings exactly.
	if resp.Document.TenantID != strconv.FormatUint(tid, 10) {
		t.Fatalf("outer document.tenant_id=%q", resp.Document.TenantID)
	}
	if resp.Document.KnowledgeBaseID != kbID || resp.Document.KnowledgeID != docID {
		t.Fatalf("outer document echo mismatch")
	}
	if resp.Document.Revision != readTestRevision {
		t.Fatalf("outer document.revision=%q", resp.Document.Revision)
	}

	// data.document tenant_id is numeric uint64.
	if resp.Data.Document.TenantID != tid {
		t.Fatalf("data.document.tenant_id=%d, want %d", resp.Data.Document.TenantID, tid)
	}
	if resp.Data.Document.KnowledgeBaseID != kbID {
		t.Fatalf("data.document.knowledge_base_id=%q", resp.Data.Document.KnowledgeBaseID)
	}
	if resp.Data.Document.Title != "Test Document" {
		t.Fatalf("data.document.title=%q", resp.Data.Document.Title)
	}
	if resp.Data.Document.Description != "A test document for read handler" {
		t.Fatalf("data.document.description=%q", resp.Data.Document.Description)
	}
	if resp.Data.Document.SourceRevision != 42 {
		t.Fatalf("data.document.source_revision=%d", resp.Data.Document.SourceRevision)
	}

	// Total, page, page_size echo.
	if resp.Data.Total != 3 {
		t.Fatalf("data.total=%d, want 3", resp.Data.Total)
	}
	if resp.Data.Page != 1 || resp.Data.PageSize != 20 {
		t.Fatalf("page echo: page=%d size=%d", resp.Data.Page, resp.Data.PageSize)
	}

	// Chunks: deterministic order, triple identity, numeric tenant_id.
	if len(resp.Data.Chunks) != 3 {
		t.Fatalf("chunks=%d, want 3", len(resp.Data.Chunks))
	}
	for i, ch := range resp.Data.Chunks {
		if ch.TenantID != tid {
			t.Fatalf("chunk[%d].tenant_id=%d, want %d", i, ch.TenantID, tid)
		}
		if ch.KnowledgeBaseID != kbID {
			t.Fatalf("chunk[%d].knowledge_base_id=%q", i, ch.KnowledgeBaseID)
		}
		if ch.KnowledgeID != docID {
			t.Fatalf("chunk[%d].knowledge_id=%q", i, ch.KnowledgeID)
		}
		if ch.ChunkIndex != i {
			t.Fatalf("chunk[%d].chunk_index=%d, want %d", i, ch.ChunkIndex, i)
		}
	}
	if resp.Data.Chunks[0].Content != "chunk-A" || resp.Data.Chunks[2].Content != "chunk-C" {
		t.Fatalf("chunks not in expected deterministic order")
	}
}

// ── Strict validation (no defaults) ──────────────────────────────────────

func TestReadAuthorizedRejectsMissingPage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupReadTestDB(t)
	tid, kbID, docID := insertReadTestData(t, db)
	engine := readTestEngine(t, db)
	body := map[string]any{
		"contract_version": 1,
		"document":         map[string]any{"tenant_id": strconv.FormatUint(tid, 10), "knowledge_base_id": kbID, "knowledge_id": docID, "revision": readTestRevision},
		"page_size":        10,
	}
	if readRequest(t, engine, body).Code != http.StatusBadRequest {
		t.Fatal("want 400 for missing page")
	}
}

func TestReadAuthorizedRejectsMissingPageSize(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupReadTestDB(t)
	tid, kbID, docID := insertReadTestData(t, db)
	engine := readTestEngine(t, db)
	body := map[string]any{
		"contract_version": 1,
		"document":         map[string]any{"tenant_id": strconv.FormatUint(tid, 10), "knowledge_base_id": kbID, "knowledge_id": docID, "revision": readTestRevision},
		"page":             1,
	}
	if readRequest(t, engine, body).Code != http.StatusBadRequest {
		t.Fatal("want 400 for missing page_size")
	}
}

func TestReadAuthorizedRejectsPageOutOfRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupReadTestDB(t)
	tid, kbID, docID := insertReadTestData(t, db)
	engine := readTestEngine(t, db)
	for _, page := range []int{0, -1, 1_000_001} {
		body := validReadBody(tid, kbID, docID)
		body["page"] = page
		body["page_size"] = 10
		if readRequest(t, engine, body).Code != http.StatusBadRequest {
			t.Fatalf("page=%d: want 400", page)
		}
	}
}

func TestReadAuthorizedRejectsPageSizeOutOfRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupReadTestDB(t)
	tid, kbID, docID := insertReadTestData(t, db)
	engine := readTestEngine(t, db)
	for _, size := range []int{0, -1, 101} {
		body := validReadBody(tid, kbID, docID)
		body["page"] = 1
		body["page_size"] = size
		if readRequest(t, engine, body).Code != http.StatusBadRequest {
			t.Fatalf("page_size=%d: want 400", size)
		}
	}
}

func TestReadAuthorizedRejectsWrongContractVersion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupReadTestDB(t)
	engine := readTestEngine(t, db)
	body := map[string]any{"contract_version": 2, "page": 1, "page_size": 10}
	if readRequest(t, engine, body).Code != http.StatusBadRequest {
		t.Fatal("want 400")
	}
}

func TestReadAuthorizedRejectsUnknownFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupReadTestDB(t)
	tid, kbID, docID := insertReadTestData(t, db)
	engine := readTestEngine(t, db)
	body := validReadBody(tid, kbID, docID)
	body["extra"] = true
	if readRequest(t, engine, body).Code != http.StatusBadRequest {
		t.Fatal("want 400 for unknown field")
	}
}

func TestReadAuthorizedRejectsTrailingAndOversizeJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupReadTestDB(t)
	engine := readTestEngine(t, db)

	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/astara/read-authorized",
		bytes.NewReader([]byte(`{"contract_version":1,"page":1,"page_size":1} {"extra":true}`)))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("Authorization", "Bearer test-service-secret")
	if (func() int { r := httptest.NewRecorder(); engine.ServeHTTP(r, req1); return r.Code })() != http.StatusBadRequest {
		t.Fatal("trailing: want 400")
	}

	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/astara/read-authorized",
		bytes.NewReader([]byte(strings.Repeat(" ", 512*1024+1)+`{}`)))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "Bearer test-service-secret")
	if (func() int { r := httptest.NewRecorder(); engine.ServeHTTP(r, req2); return r.Code })() != http.StatusBadRequest {
		t.Fatal("oversize: want 400")
	}
}

// ── Document field validation ────────────────────────────────────────────

func TestReadAuthorizedRejectsEmptyDocFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupReadTestDB(t)
	tid, kbID, docID := insertReadTestData(t, db)
	engine := readTestEngine(t, db)
	for _, field := range []string{"tenant_id", "knowledge_base_id", "knowledge_id", "revision"} {
		body := validReadBody(tid, kbID, docID)
		body["document"].(map[string]any)[field] = ""
		if readRequest(t, engine, body).Code != http.StatusBadRequest {
			t.Fatalf("empty %s: want 400", field)
		}
	}
}

func TestReadAuthorizedRejectsSurroundingWhitespace(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupReadTestDB(t)
	tid, kbID, docID := insertReadTestData(t, db)
	engine := readTestEngine(t, db)
	for _, field := range []string{"knowledge_base_id", "knowledge_id", "revision"} {
		body := validReadBody(tid, kbID, docID)
		body["document"].(map[string]any)[field] = " " + body["document"].(map[string]any)[field].(string) + " "
		if readRequest(t, engine, body).Code != http.StatusBadRequest {
			t.Fatalf("whitespace %s: want 400", field)
		}
	}
}

func TestReadAuthorizedRejectsNonCanonicalTenantID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupReadTestDB(t)
	_, kbID, docID := insertReadTestData(t, db)
	engine := readTestEngine(t, db)
	for _, bad := range []string{"07", "007", "+7", " 7", "7 "} {
		body := validReadBody(0, kbID, docID)
		body["document"].(map[string]any)["tenant_id"] = bad
		if readRequest(t, engine, body).Code != http.StatusBadRequest {
			t.Fatalf("tenant_id=%q: want 400", bad)
		}
	}
}

func TestReadAuthorizedRejectsNonNumericTenantID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupReadTestDB(t)
	_, kbID, docID := insertReadTestData(t, db)
	engine := readTestEngine(t, db)
	body := validReadBody(0, kbID, docID)
	body["document"].(map[string]any)["tenant_id"] = "not-a-number"
	if readRequest(t, engine, body).Code != http.StatusBadRequest {
		t.Fatal("want 400")
	}
}

func TestReadAuthorizedRejectsMalformedRevision(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupReadTestDB(t)
	tid, kbID, docID := insertReadTestData(t, db)
	engine := readTestEngine(t, db)
	for _, rev := range []string{
		"abcd1234",                    // too short
		strings.Repeat("z", 64),       // not hex
		strings.Repeat("a", 63),       // wrong length
		strings.Repeat("A", 64),       // uppercase hex
		strings.Repeat("a", 64) + " ", // trailing space
		" " + strings.Repeat("a", 64), // leading space
	} {
		body := validReadBody(tid, kbID, docID)
		body["document"].(map[string]any)["revision"] = rev
		if readRequest(t, engine, body).Code != http.StatusBadRequest {
			t.Fatalf("revision=%q: want 400", rev)
		}
	}
}

// ── Not found / wrong scope ──────────────────────────────────────────────

func TestReadAuthorizedRejectsWrongTenant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupReadTestDB(t)
	_, kbID, docID := insertReadTestData(t, db)
	engine := readTestEngine(t, db)
	if readRequest(t, engine, validReadBody(99999, kbID, docID)).Code != http.StatusNotFound {
		t.Fatal("want 404")
	}
}

func TestReadAuthorizedRejectsWrongKB(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupReadTestDB(t)
	tid, _, docID := insertReadTestData(t, db)
	engine := readTestEngine(t, db)
	if readRequest(t, engine, validReadBody(tid, "kb-nonexistent", docID)).Code != http.StatusNotFound {
		t.Fatal("want 404")
	}
}

func TestReadAuthorizedRejectsWrongDoc(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupReadTestDB(t)
	tid, kbID, _ := insertReadTestData(t, db)
	engine := readTestEngine(t, db)
	if readRequest(t, engine, validReadBody(tid, kbID, "doc-nonexistent")).Code != http.StatusNotFound {
		t.Fatal("want 404")
	}
}

func TestReadAuthorizedRejectsDeletedDoc(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupReadTestDB(t)
	tid, kbID, docID := insertReadTestData(t, db)
	engine := readTestEngine(t, db)
	db.Model(&types.Knowledge{}).Where("id = ?", docID).Update("deleted_at", time.Now())
	if readRequest(t, engine, validReadBody(tid, kbID, docID)).Code != http.StatusNotFound {
		t.Fatal("want 404 for deleted doc")
	}
}

func TestReadAuthorizedRejectsDeletedKB(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupReadTestDB(t)
	tid, kbID, docID := insertReadTestData(t, db)
	engine := readTestEngine(t, db)
	db.Model(&types.KnowledgeBase{}).Where("id = ?", kbID).Update("deleted_at", time.Now())
	if readRequest(t, engine, validReadBody(tid, kbID, docID)).Code != http.StatusNotFound {
		t.Fatal("want 404 for deleted KB")
	}
}

func TestReadAuthorizedRejectsNonQueryableDoc(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupReadTestDB(t)
	tid, kbID, docID := insertReadTestData(t, db)
	engine := readTestEngine(t, db)
	for _, state := range []string{"pending", "processing", "failed", "deleting"} {
		db.Model(&types.Knowledge{}).Where("id = ?", docID).Update("parse_status", state)
		if readRequest(t, engine, validReadBody(tid, kbID, docID)).Code != http.StatusConflict {
			t.Fatalf("parse_status=%s: want 409", state)
		}
	}
	db.Model(&types.Knowledge{}).Where("id = ?", docID).Updates(map[string]any{
		"parse_status": types.ParseStatusCompleted, "enable_status": "disabled",
	})
	if readRequest(t, engine, validReadBody(tid, kbID, docID)).Code != http.StatusConflict {
		t.Fatal("disabled: want 409")
	}
}

// ── Pagination ───────────────────────────────────────────────────────────

func TestReadAuthorizedPaging(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupReadTestDB(t)
	tid, kbID, docID := insertReadTestData(t, db)
	engine := readTestEngine(t, db)

	body := validReadBody(tid, kbID, docID)
	body["page"] = 1
	body["page_size"] = 2
	recorder := readRequest(t, engine, body)
	var resp astaraReadAuthorizedResponse
	json.Unmarshal(recorder.Body.Bytes(), &resp)
	if resp.Data.Total != 3 || len(resp.Data.Chunks) != 2 {
		t.Fatalf("page1: total=%d chunks=%d", resp.Data.Total, len(resp.Data.Chunks))
	}
	if resp.Data.Page != 1 || resp.Data.PageSize != 2 {
		t.Fatalf("page echo: page=%d size=%d", resp.Data.Page, resp.Data.PageSize)
	}

	body["page"] = 2
	recorder = readRequest(t, engine, body)
	json.Unmarshal(recorder.Body.Bytes(), &resp)
	if len(resp.Data.Chunks) != 1 || resp.Data.Chunks[0].Content != "chunk-C" {
		t.Fatalf("page2: chunks=%d content=%q", len(resp.Data.Chunks), resp.Data.Chunks[0].Content)
	}

	body["page"] = 3
	recorder = readRequest(t, engine, body)
	json.Unmarshal(recorder.Body.Bytes(), &resp)
	if len(resp.Data.Chunks) != 0 || resp.Data.Total != 3 {
		t.Fatalf("page3: chunks=%d total=%d", len(resp.Data.Chunks), resp.Data.Total)
	}
}

// ── Empty doc ────────────────────────────────────────────────────────────

func TestReadAuthorizedEmptyDocReturnsZeroTotal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupReadTestDB(t)
	tenant := &types.Tenant{Name: "Empty", Status: "active", Business: "astara"}
	db.Create(tenant)
	tid := tenant.ID
	kbID := "kb-empty"
	db.Create(&types.KnowledgeBase{
		ID: kbID, Name: "Empty", Type: types.KnowledgeBaseTypeDocument,
		TenantID: tid, EmbeddingModelID: "e", SummaryModelID: "s",
	})
	docID := "doc-empty"
	db.Create(&types.Knowledge{
		ID: docID, TenantID: tid, KnowledgeBaseID: kbID,
		ParseStatus: types.ParseStatusCompleted, EnableStatus: "enabled",
	})
	engine := readTestEngine(t, db)
	recorder := readRequest(t, engine, validReadBody(tid, kbID, docID))
	var resp astaraReadAuthorizedResponse
	json.Unmarshal(recorder.Body.Bytes(), &resp)
	if resp.Data.Total != 0 || len(resp.Data.Chunks) != 0 {
		t.Fatalf("empty: total=%d chunks=%d", resp.Data.Total, len(resp.Data.Chunks))
	}
}

// ── Service auth ─────────────────────────────────────────────────────────

func TestReadAuthorizedRequiresServiceAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupReadTestDB(t)
	engine := readTestEngine(t, db)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/astara/read-authorized",
		bytes.NewReader([]byte(`{"contract_version":1,"page":1,"page_size":1}`)))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401", recorder.Code)
	}
}

// ── Post-read revalidation: tenant/KB/doc drift ──────────────────────────

func TestReadAuthorizedDetectsTenantDisabledDuringRead(t *testing.T) {
	testReadDrift(t, "tenant-disabled", func(db *gorm.DB, tid uint64, kbID, docID string) {
		db.Model(&types.Tenant{}).Where("id = ?", tid).Update("status", "suspended")
	})
}

func TestReadAuthorizedDetectsKBDeletedDuringRead(t *testing.T) {
	testReadDrift(t, "kb-deleted", func(db *gorm.DB, tid uint64, kbID, docID string) {
		db.Model(&types.KnowledgeBase{}).Where("id = ?", kbID).Update("deleted_at", time.Now())
	})
}

func TestReadAuthorizedDetectsKBTenantChangedDuringRead(t *testing.T) {
	testReadDrift(t, "kb-tenant", func(db *gorm.DB, tid uint64, kbID, docID string) {
		newT := &types.Tenant{Name: "Moved", Status: "active", Business: "astara"}
		db.Create(newT)
		db.Model(&types.KnowledgeBase{}).Where("id = ?", kbID).Update("tenant_id", newT.ID)
	})
}

func TestReadAuthorizedDetectsDocDeletedDuringRead(t *testing.T) {
	testReadDrift(t, "doc-deleted", func(db *gorm.DB, tid uint64, kbID, docID string) {
		db.Model(&types.Knowledge{}).Where("id = ?", docID).Update("deleted_at", time.Now())
	})
}

func TestReadAuthorizedDetectsDocDisabledDuringRead(t *testing.T) {
	testReadDrift(t, "doc-disabled", func(db *gorm.DB, tid uint64, kbID, docID string) {
		db.Model(&types.Knowledge{}).Where("id = ?", docID).Update("enable_status", "disabled")
	})
}

func TestReadAuthorizedDetectsDocParseFailedDuringRead(t *testing.T) {
	testReadDrift(t, "doc-parse-failed", func(db *gorm.DB, tid uint64, kbID, docID string) {
		db.Model(&types.Knowledge{}).Where("id = ?", docID).Update("parse_status", "failed")
	})
}

func TestReadAuthorizedDetectsDocKBChangedDuringRead(t *testing.T) {
	testReadDrift(t, "doc-kb-changed", func(db *gorm.DB, tid uint64, kbID, docID string) {
		db.Model(&types.Knowledge{}).Where("id = ?", docID).Update("knowledge_base_id", "kb-other")
	})
}

func TestReadAuthorizedDetectsDocRevisionChangedDuringRead(t *testing.T) {
	testReadDrift(t, "doc-revision", func(db *gorm.DB, tid uint64, kbID, docID string) {
		db.Model(&types.Knowledge{}).Where("id = ?", docID).Update("source_revision", 999)
	})
}

func TestReadAuthorizedDetectsDocUpdatedChangedDuringRead(t *testing.T) {
	testReadDrift(t, "doc-updated", func(db *gorm.DB, tid uint64, kbID, docID string) {
		db.Model(&types.Knowledge{}).Where("id = ?", docID).Update("updated_at", time.Now().Add(time.Hour))
	})
}

func TestReadAuthorizedDetectsDocTenantChangedDuringRead(t *testing.T) {
	testReadDrift(t, "doc-tenant", func(db *gorm.DB, tid uint64, kbID, docID string) {
		newT := &types.Tenant{Name: "DocMoved", Status: "active", Business: "astara"}
		db.Create(newT)
		db.Model(&types.Knowledge{}).Where("id = ?", docID).Update("tenant_id", newT.ID)
	})
}

// testReadDrift inserts baseline data, then uses a GORM callback that fires
// on the 4th query (chunk count, after snapshot) to apply the mutation. The
// revalidation queries then see the drift and return 409 with no data leak.
func testReadDrift(t *testing.T, name string, mutate func(*gorm.DB, uint64, string, string)) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dsn := "file:astara-read-drift-" + name + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&types.Tenant{}, &types.KnowledgeBase{}, &types.Knowledge{}, &types.Chunk{}); err != nil {
		t.Fatal(err)
	}
	tid, kbID, docID := insertReadTestData(t, db)

	var queryCount int
	db.Callback().Query().Before("gorm:query").Register("drift:"+name, func(tx *gorm.DB) {
		queryCount++
		if queryCount == 4 {
			mutate(db, tid, kbID, docID)
		}
	})

	engine := readTestEngine(t, db)
	recorder := readRequest(t, engine, validReadBody(tid, kbID, docID))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("mutation=%s status=%d body=%s, want 409", name, recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, `"chunks"`) || strings.Contains(body, `"total"`) {
		t.Fatalf("mutation=%s: data leaked on conflict: %s", name, body)
	}
}

// ── No storage leakage ───────────────────────────────────────────────────

func TestReadAuthorizedNoStorageLeakage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupReadTestDB(t)
	tid, kbID, docID := insertReadTestData(t, db)
	engine := readTestEngine(t, db)
	db.Model(&types.Knowledge{}).Where("id = ?", docID).Update("file_path", "local://tenant/secret-file.pdf")
	recorder := readRequest(t, engine, validReadBody(tid, kbID, docID))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, forbidden := range []string{"file_path", "secret-file", "storage", "signed_url", "content_hash"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response leaked %q", forbidden)
		}
	}
}
