package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
)

func astaraTestEngine(t *testing.T) *gin.Engine {
	t.Helper()
	t.Setenv(astaraServiceAuthEnv, "test-service-secret")
	db, err := gorm.Open(sqlite.Open("file:astara-control-plane?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&types.Tenant{}, &types.KnowledgeBase{}); err != nil {
		t.Fatal(err)
	}
	h := NewAstaraControlPlaneHandler(db)
	r := gin.New()
	g := r.Group("/api/v1/astara", h.Authenticate)
	g.GET("/tenants/by-external-id", h.FindTenant)
	g.POST("/tenants", h.CreateTenant)
	g.GET("/knowledge-bases/by-external-id", h.FindKnowledgeBase)
	g.POST("/knowledge-bases", h.CreateKnowledgeBase)
	return r
}

func astaraRequest(t *testing.T, engine *gin.Engine, method, path string, body any, authenticated bool) *httptest.ResponseRecorder {
	t.Helper()
	var encoded []byte
	if body != nil {
		var err error
		encoded, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	if authenticated {
		req.Header.Set("Authorization", "Bearer test-service-secret")
	}
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	return recorder
}

func decodeAstaraResource(t *testing.T, recorder *httptest.ResponseRecorder) astaraProviderResource {
	t.Helper()
	var resource astaraProviderResource
	if err := json.Unmarshal(recorder.Body.Bytes(), &resource); err != nil {
		t.Fatal(err)
	}
	if resource.ID == "" || resource.ExternalSystem == "" || resource.ExternalID == "" {
		t.Fatalf("invalid provider wire shape: %s", recorder.Body.String())
	}
	return resource
}

func TestAstaraControlPlaneAuthAndIdempotentWireContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := astaraTestEngine(t)
	body := map[string]any{"external_system": "astara", "external_id": "workspace-1", "name": "Workspace"}
	unauthorized := astaraRequest(t, engine, http.MethodPost, "/api/v1/astara/tenants", body, false)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}

	created := astaraRequest(t, engine, http.MethodPost, "/api/v1/astara/tenants", body, true)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	tenant := decodeAstaraResource(t, created)
	if _, err := strconv.ParseUint(tenant.ID, 10, 64); err != nil {
		t.Fatalf("tenant id must be a numeric string: %q", tenant.ID)
	}

	replayed := astaraRequest(t, engine, http.MethodPost, "/api/v1/astara/tenants", body, true)
	if replayed.Code != http.StatusOK || decodeAstaraResource(t, replayed).ID != tenant.ID {
		t.Fatalf("tenant retry did not converge: %d %s", replayed.Code, replayed.Body.String())
	}

	kbBody := map[string]any{"tenant_id": tenant.ID, "external_system": "astara", "external_id": "space-1", "name": "Space"}
	kbCreated := astaraRequest(t, engine, http.MethodPost, "/api/v1/astara/knowledge-bases", kbBody, true)
	if kbCreated.Code != http.StatusCreated {
		t.Fatalf("kb create status=%d body=%s", kbCreated.Code, kbCreated.Body.String())
	}
	kb := decodeAstaraResource(t, kbCreated)
	lookup := astaraRequest(t, engine, http.MethodGet, "/api/v1/astara/knowledge-bases/by-external-id?tenant_id="+tenant.ID+"&external_system=astara&external_id=space-1", nil, true)
	if lookup.Code != http.StatusOK || decodeAstaraResource(t, lookup).ID != kb.ID {
		t.Fatalf("kb lookup mismatch: %d %s", lookup.Code, lookup.Body.String())
	}
}
