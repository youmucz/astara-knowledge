package router

import (
	"sort"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/astara"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/types"
)

func TestKnowledgeWorkerQueuesExcludeExecutionFamilies(t *testing.T) {
	t.Setenv(astara.ProfileEnv, astara.KnowledgeProfile)
	for _, test := range []struct {
		pool   string
		shared bool
	}{
		{types.WorkerPoolCore, false},
		{types.WorkerPoolEnrichment, false},
		{types.WorkerPoolShared, true},
	} {
		weights := effectiveQueueWeights(test.pool, test.shared)
		for _, prohibited := range []string{types.QueueChatAttachment, types.QueueGraph, types.QueueMemory} {
			if _, found := weights[prohibited]; found {
				t.Fatalf("pool %s subscribes to prohibited queue %s: %v", test.pool, prohibited, weights)
			}
		}
	}
}

func TestAstaraControlPlaneRouteInventory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterAstaraControlPlaneRoutes(r.Group("/api/v1"), &handler.AstaraControlPlaneHandler{})
	got := make([]string, 0)
	for _, route := range r.Routes() {
		got = append(got, route.Method+" "+route.Path)
	}
	sort.Strings(got)
	want := []string{
		"DELETE /api/v1/astara/tenants/:tenant_id",
		"DELETE /api/v1/astara/tenants/:tenant_id/knowledge-bases/:knowledge_base_id",
		"GET /api/v1/astara/knowledge-bases/by-external-id",
		"GET /api/v1/astara/tenants/by-external-id",
		"POST /api/v1/astara/knowledge-bases",
		"POST /api/v1/astara/tenants",
	}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("routes=%v, want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("routes=%v, want=%v", got, want)
		}
	}
}

// TestAstaraIdentityRouteInventory pins the embedded identity exchange
// surface: exchange is assertion-authenticated, revoke is
// service-authenticated, and nothing else lives under /identity.
func TestAstaraIdentityRouteInventory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	t.Setenv("ASTARA_SERVICE_AUTH_SECRET", "test-service-secret")
	RegisterAstaraIdentityRoutes(r.Group("/api/v1"), &handler.AstaraIdentityExchangeHandler{})
	got := make([]string, 0)
	for _, route := range r.Routes() {
		got = append(got, route.Method+" "+route.Path)
	}
	sort.Strings(got)
	want := []string{
		"POST /api/v1/astara/identity/exchange",
		"POST /api/v1/astara/identity/revoke",
	}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("routes=%v, want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("routes=%v, want=%v", got, want)
		}
	}
}
