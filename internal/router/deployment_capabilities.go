package router

import (
	"strings"

	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/gin-gonic/gin"
)

func deploymentCapabilitiesFromRouter(params RouterParams) handler.DeploymentCapabilitiesData {
	return handler.BuildDeploymentCapabilities(handler.Edition, handler.DeploymentFeatureAvailability{
		Organizations: params.OrganizationHandler != nil,
		Agents:        params.CustomAgentHandler != nil,
		IM:            params.IMHandler != nil,
		// Match RegisterEmbedChannelRoutes: management routes depend on handler only.
		Embed: params.EmbedChannelHandler != nil,
		API:   params.TenantHandler != nil && params.TenantAPIKeyService != nil,
		MCP: params.MCPServiceHandler != nil &&
			params.MCPCredentialsHandler != nil &&
			params.MCPOAuthHandler != nil,
		WebSearch: params.WebSearchHandler != nil &&
			params.WebSearchProviderHandler != nil &&
			params.WebSearchCredentialsHandler != nil,
		VectorStore:   params.VectorStoreHandler != nil,
		Storage:       params.StorageBackendHandler != nil,
		Sandbox:       params.SandboxConfigHandler != nil,
		SandboxDocker: sandbox.DockerBackendEnabled(),
	})
}

// deploymentCapabilitiesFromRoutes makes the effective Gin route table the
// source of truth. A constructed handler can no longer advertise a feature
// whose routes were omitted by the closed profile.
func deploymentCapabilitiesFromRoutes(r *gin.Engine) handler.DeploymentCapabilitiesData {
	routes := r.Routes()
	has := func(parts ...string) bool {
		for _, route := range routes {
			for _, part := range parts {
				if strings.Contains(route.Path, part) {
					return true
				}
			}
		}
		return false
	}
	return handler.BuildDeploymentCapabilities(handler.Edition, handler.DeploymentFeatureAvailability{
		Organizations: has("/organizations"),
		Agents:        has("/agents", "/agent/"),
		IM:            has("/im/", "/im-channels"),
		Embed:         has("/embed-channels", "/embed/"),
		API:           has("/tenant-api-keys", "/platform-api-keys"),
		MCP:           has("/mcp-services", "/mcp/"),
		WebSearch:     has("/web-search", "/web-search-providers"),
		VectorStore:   has("/vector-stores"),
		Storage:       has("/storage-backends"),
		Sandbox:       has("/sandbox"),
		SandboxDocker: has("/sandbox") && sandbox.DockerBackendEnabled(),
	})
}
