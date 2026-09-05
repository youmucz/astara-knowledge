package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
)

func astaraDocumentTestEngine(t *testing.T) (*gin.Engine, *gorm.DB, string) {
	t.Helper()
	t.Setenv(astaraServiceAuthEnv, "test-service-secret")
	dsn := "file:astara-documents-" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&types.Tenant{}, &types.KnowledgeBase{}, &types.Knowledge{}, &types.Chunk{}); err != nil {
		t.Fatal(err)
	}
	tenant := &types.Tenant{Name: "T", Status: "active", Business: "astara"}
	if err := db.Create(tenant).Error; err != nil {
		t.Fatal(err)
	}
	kb := &types.KnowledgeBase{ID: "kb-1", Name: "Space", Type: types.KnowledgeBaseTypeDocument, TenantID: tenant.ID}
	if err := db.Create(kb).Error; err != nil {
		t.Fatal(err)
	}
	h := NewAstaraControlPlaneHandler(db)
	r := gin.New()
	g := r.Group("/api/v1/astara", h.Authenticate)
	g.PUT("/knowledge-bases/:knowledge_base_id/documents", h.UpsertDocument)
	g.GET("/knowledge-bases/:knowledge_base_id/documents/by-external-id", h.FindDocumentByExternalID)
	g.GET("/knowledge-bases/:knowledge_base_id/documents/:document_id", h.FindDocument)
	g.DELETE("/knowledge-bases/:knowledge_base_id/documents/:document_id", h.DeleteDocument)
	return r, db, kb.ID
}

func upsertBody(externalID string, revision int64, content string) map[string]any {
	return map[string]any{
		"external_system":     "astara",
		"external_id":         externalID,
		"title":               "Page " + externalID,
		"content":             content,
		"content_hash":        "hash-" + externalID + "-" + string(rune('a'+revision%26)),
		"source_revision":     revision,
		"policy_digest":       "policy-1",
		"project_ids":         []string{"proj-1", "proj-2"},
		"label_ids":           []string{"label-1"},
		"canonical_reference": "plane-page:" + externalID,
	}
}

func TestAstaraDocumentUpsertCreatesSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine, db, kbID := astaraDocumentTestEngine(t)
	created := astaraRequest(t, engine, http.MethodPut, "/api/v1/astara/knowledge-bases/"+kbID+"/documents", upsertBody("page-1", 100, "hello world"), true)
	if created.Code != http.StatusOK {
		t.Fatalf("upsert status=%d body=%s", created.Code, created.Body.String())
	}
	var resource astaraDocumentResource
	if err := json.Unmarshal(created.Body.Bytes(), &resource); err != nil {
		t.Fatal(err)
	}
	if resource.ID == "" || resource.ExternalID != "page-1" || resource.SourceRevision != 100 {
		t.Fatalf("unexpected resource: %+v", resource)
	}
	var chunkCount int64
	db.Model(&types.Chunk{}).Where("knowledge_id = ?", resource.ID).Count(&chunkCount)
	if chunkCount == 0 {
		t.Fatal("upsert must persist chunk rows")
	}
}

func TestAstaraDocumentUpsertIsIdempotentPerIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine, db, kbID := astaraDocumentTestEngine(t)
	first := astaraRequest(t, engine, http.MethodPut, "/api/v1/astara/knowledge-bases/"+kbID+"/documents", upsertBody("page-1", 100, "v1"), true)
	second := astaraRequest(t, engine, http.MethodPut, "/api/v1/astara/knowledge-bases/"+kbID+"/documents", upsertBody("page-1", 100, "v1"), true)
	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("idempotent replay failed: %d %d", first.Code, second.Code)
	}
	var one, two astaraDocumentResource
	json.Unmarshal(first.Body.Bytes(), &one)
	json.Unmarshal(second.Body.Bytes(), &two)
	if one.ID != two.ID {
		t.Fatalf("same identity must map to one provider record: %s vs %s", one.ID, two.ID)
	}
	var count int64
	db.Model(&types.Knowledge{}).Where("knowledge_base_id = ?", kbID).Count(&count)
	if count != 1 {
		t.Fatalf("duplicate identities created: %d", count)
	}
}

func TestAstaraDocumentUpsertReplacesObsoleteChunksAtomically(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine, db, kbID := astaraDocumentTestEngine(t)
	first := astaraRequest(t, engine, http.MethodPut, "/api/v1/astara/knowledge-bases/"+kbID+"/documents", upsertBody("page-1", 100, strings.Repeat("a", 2500)), true)
	if first.Code != http.StatusOK {
		t.Fatalf("first upsert failed: %d", first.Code)
	}
	var one astaraDocumentResource
	json.Unmarshal(first.Body.Bytes(), &one)
	var before int64
	db.Model(&types.Chunk{}).Where("knowledge_id = ?", one.ID).Count(&before)
	if before < 2 {
		t.Fatalf("expected multiple chunks, got %d", before)
	}

	updated := astaraRequest(t, engine, http.MethodPut, "/api/v1/astara/knowledge-bases/"+kbID+"/documents", upsertBody("page-1", 200, "short"), true)
	if updated.Code != http.StatusOK {
		t.Fatalf("update upsert failed: %d body=%s", updated.Code, updated.Body.String())
	}
	var two astaraDocumentResource
	json.Unmarshal(updated.Body.Bytes(), &two)
	if two.ID == one.ID {
		// Replacement is allowed to reuse or replace the row id, but obsolete
		// chunk rows must not survive.
		var stale int64
		db.Model(&types.Chunk{}).Where("knowledge_id = ?", one.ID).Count(&stale)
		if two.ID == one.ID && stale > 1 {
			t.Fatalf("obsolete chunks were not replaced: %d", stale)
		}
	}
	var after int64
	db.Model(&types.Chunk{}).Where("knowledge_id = ?", two.ID).Count(&after)
	if after != 1 {
		t.Fatalf("replacement must store exactly the new snapshot chunks, got %d", after)
	}
	var total int64
	db.Model(&types.Knowledge{}).Where("knowledge_base_id = ? AND external_id = ?", kbID, "page-1").Count(&total)
	if total != 1 {
		t.Fatalf("one identity must remain one document, got %d", total)
	}
}

func TestAstaraDocumentUpsertRejectsStaleRevision(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine, _, kbID := astaraDocumentTestEngine(t)
	if code := astaraRequest(t, engine, http.MethodPut, "/api/v1/astara/knowledge-bases/"+kbID+"/documents", upsertBody("page-1", 200, "newer"), true).Code; code != http.StatusOK {
		t.Fatalf("initial upsert failed: %d", code)
	}
	stale := astaraRequest(t, engine, http.MethodPut, "/api/v1/astara/knowledge-bases/"+kbID+"/documents", upsertBody("page-1", 150, "older late arrival"), true)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale revision must be fenced with 409, got %d", stale.Code)
	}
	var stored astaraDocumentResource
	lookup := astaraRequest(t, engine, http.MethodGet, "/api/v1/astara/knowledge-bases/"+kbID+"/documents/by-external-id?external_system=astara&external_id=page-1", nil, true)
	json.Unmarshal(lookup.Body.Bytes(), &stored)
	if stored.SourceRevision != 200 {
		t.Fatalf("provider retained wrong revision: %+v", stored)
	}
}

func TestAstaraDocumentFindAndDelete(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine, db, kbID := astaraDocumentTestEngine(t)
	created := astaraRequest(t, engine, http.MethodPut, "/api/v1/astara/knowledge-bases/"+kbID+"/documents", upsertBody("page-1", 10, "content"), true)
	var resource astaraDocumentResource
	json.Unmarshal(created.Body.Bytes(), &resource)

	byID := astaraRequest(t, engine, http.MethodGet, "/api/v1/astara/knowledge-bases/"+kbID+"/documents/"+resource.ID, nil, true)
	if byID.Code != http.StatusOK {
		t.Fatalf("find by id failed: %d", byID.Code)
	}

	deleted := astaraRequest(t, engine, http.MethodDelete, "/api/v1/astara/knowledge-bases/"+kbID+"/documents/"+resource.ID, nil, true)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete failed: %d", deleted.Code)
	}
	var chunks int64
	db.Model(&types.Chunk{}).Where("knowledge_id = ?", resource.ID).Count(&chunks)
	if chunks != 0 {
		t.Fatalf("delete must remove chunk rows, got %d", chunks)
	}
	again := astaraRequest(t, engine, http.MethodDelete, "/api/v1/astara/knowledge-bases/"+kbID+"/documents/"+resource.ID, nil, true)
	if again.Code != http.StatusNotFound {
		t.Fatalf("delete must be idempotent-tolerant (404), got %d", again.Code)
	}
}

func TestAstaraDocumentUpsertValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine, _, kbID := astaraDocumentTestEngine(t)
	if code := astaraRequest(t, engine, http.MethodPut, "/api/v1/astara/knowledge-bases/kb-missing/documents", upsertBody("page-1", 1, "x"), true).Code; code != http.StatusNotFound {
		t.Fatalf("unknown kb must 404, got %d", code)
	}
	emptyContent := upsertBody("page-2", 1, "")
	if code := astaraRequest(t, engine, http.MethodPut, "/api/v1/astara/knowledge-bases/"+kbID+"/documents", emptyContent, true).Code; code != http.StatusBadRequest {
		t.Fatalf("empty content must 400, got %d", code)
	}
	unauthenticated := astaraRequest(t, engine, http.MethodPut, "/api/v1/astara/knowledge-bases/"+kbID+"/documents", upsertBody("page-3", 1, "x"), false)
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("document seam requires service auth, got %d", unauthenticated.Code)
	}
}

func TestAstaraDocumentOneIdentityAcrossProjects(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine, db, kbID := astaraDocumentTestEngine(t)
	// The same page gains a second project link: project metadata changes but
	// the identity stays one record.
	body := upsertBody("page-1", 100, "content")
	first := astaraRequest(t, engine, http.MethodPut, "/api/v1/astara/knowledge-bases/"+kbID+"/documents", body, true)
	if first.Code != http.StatusOK {
		t.Fatalf("first upsert failed: %d", first.Code)
	}
	body["project_ids"] = []string{"proj-1", "proj-2", "proj-3"}
	second := astaraRequest(t, engine, http.MethodPut, "/api/v1/astara/knowledge-bases/"+kbID+"/documents", body, true)
	if second.Code != http.StatusOK {
		t.Fatalf("metadata-only upsert failed: %d", second.Code)
	}
	var count int64
	db.Model(&types.Knowledge{}).Where("external_id = ?", "page-1").Count(&count)
	if count != 1 {
		t.Fatalf("multi-project page must stay one provider document, got %d", count)
	}
}
