package router

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/astara"
)

func TestDisabledProfileRoutesReturnNotFoundBeforeAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	installProfileBoundary(engine, astara.Profile{Name: astara.KnowledgeProfile, Valid: true})
	// Model the global user authentication middleware that follows the profile
	// boundary in NewRouter. Unauthenticated non-profile requests become 401.
	engine.Use(func(c *gin.Context) {
		if c.GetHeader("Authorization") == "" {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Next()
	})

	paths := []string{
		"/api/v1/agents", "/api/v1/skills", "/api/v1/sandbox-configs",
		"/api/v1/mcp-services", "/api/v1/web-search/providers",
		"/api/v1/im/channels", "/api/v1/memory", "/api/v1/evaluation",
		"/api/v1/tools/run", "/api/v1/graph/query",
		"/api/v1/me/env-vars", "/api/v1/tenants/kv/memory-config",
		"/api/v1/tenants/kv/web-search-config", "/api/v1/system/sandbox-check",
		"/api/v1/organizations/1/agent-shares",
	}
	for _, path := range paths {
		for _, authenticated := range []bool{false, true} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			if authenticated {
				req.Header.Set("Authorization", "Bearer user-token")
			}
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("path=%s authenticated=%t status=%d, want 404", path, authenticated, recorder.Code)
			}
		}
	}

	normal := httptest.NewRecorder()
	engine.ServeHTTP(normal, httptest.NewRequest(http.MethodGet, "/api/v1/knowledge-bases", nil))
	if normal.Code != http.StatusUnauthorized {
		t.Fatalf("normal unauthenticated route status=%d, want auth middleware 401", normal.Code)
	}
}

func TestUnknownProfileNewRouterExposesOnlyHealthRoutes(t *testing.T) {
	t.Setenv(astara.ProfileEnv, "unknown-profile")
	gin.SetMode(gin.TestMode)
	engine := NewRouter(RouterParams{})
	got := make([]string, 0, len(engine.Routes()))
	for _, route := range engine.Routes() {
		got = append(got, route.Method+" "+route.Path)
	}
	sort.Strings(got)
	want := []string{"GET /health", "GET /health/live", "GET /health/ready"}
	if len(got) != len(want) {
		t.Fatalf("unknown profile routes=%v, want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unknown profile routes=%v, want=%v", got, want)
		}
	}

	for _, authenticated := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/agents", nil)
		if authenticated {
			req.Header.Set("Authorization", "Bearer anything")
		}
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("unknown profile authenticated=%t status=%d, want 404", authenticated, recorder.Code)
		}
	}
}
