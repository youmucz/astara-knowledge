package router

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/astara"
)

// disabledRoutePrefixes is the HTTP boundary for execution-oriented modules.
// It is installed before user authentication so route absence cannot leak as
// a credential-dependent 401/403. The effective Gin route table still omits
// these endpoints; this middleware only normalizes unmatched requests to 404.
var disabledRoutePrefixes = []string{
	"/api/v1/agent",
	"/api/v1/agents",
	"/api/v1/chat",
	"/api/v1/sessions",
	"/api/v1/messages",
	"/api/v1/skills",
	"/api/v1/sandbox",
	"/api/v1/mcp",
	"/api/v1/web-search",
	"/api/v1/im",
	"/api/v1/embed",
	"/api/v1/memory",
	"/api/v1/memories",
	"/api/v1/evaluation",
	"/api/v1/evaluations",
	"/api/v1/data-analysis",
	"/api/v1/tools",
	"/api/v1/graph",
	"/api/v1/me/env-vars",
	"/api/v1/tenants/kv/memory-config",
	"/api/v1/tenants/kv/web-search-config",
	"/api/v1/system/sandbox-check",
}

var disabledRouteFragments = []string{"/agent-shares", "/shared-agents"}

func installProfileBoundary(r *gin.Engine, profile astara.Profile) {
	if !profile.Valid {
		return
	}
	r.Use(func(c *gin.Context) {
		path := c.Request.URL.Path
		for _, prefix := range disabledRoutePrefixes {
			if path == prefix || strings.HasPrefix(path, prefix+"/") || strings.HasPrefix(path, prefix+"-") {
				c.AbortWithStatus(http.StatusNotFound)
				return
			}
		}
		for _, fragment := range disabledRouteFragments {
			if strings.Contains(path, fragment) {
				c.AbortWithStatus(http.StatusNotFound)
				return
			}
		}
		c.Next()
	})
}
