package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/astara"
)

func TestAstaraHealthWireContractAndDependencyFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	profile := astara.Profile{Name: astara.KnowledgeProfile, Valid: true}
	registerAstaraHealthRoutes(engine, nil, nil, profile)

	live := httptest.NewRecorder()
	engine.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if live.Code != http.StatusOK || live.Body.String() != "{\"live\":true}" {
		t.Fatalf("liveness response=%d %s", live.Code, live.Body.String())
	}

	ready := httptest.NewRecorder()
	engine.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if ready.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness without dependencies=%d, want 503", ready.Code)
	}
	var body struct {
		Ready    bool            `json:"ready"`
		Identity astara.Identity `json:"identity"`
	}
	if err := json.Unmarshal(ready.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Ready || body.Identity != astara.ReleaseIdentity(profile) {
		t.Fatalf("readiness wire drift: %+v", body)
	}
}

func TestUnknownProfileReadinessFailsClosed(t *testing.T) {
	engine := gin.New()
	profile := astara.Profile{Name: "unknown", Valid: false}
	registerAstaraHealthRoutes(engine, nil, nil, profile)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unknown profile readiness=%d, want 503", recorder.Code)
	}
}
