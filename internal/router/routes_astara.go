package router

import (
	"net/http"

	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/gin-gonic/gin"
)

func RegisterAstaraControlPlaneRoutes(r *gin.RouterGroup, h *handler.AstaraControlPlaneHandler) {
	if h == nil {
		return
	}
	group := r.Group("/astara", h.Authenticate)
	group.GET("/tenants/by-external-id", h.FindTenant)
	group.POST("/tenants", h.CreateTenant)
	group.DELETE("/tenants/:tenant_id", h.DeleteTenant)
	group.GET("/knowledge-bases/by-external-id", h.FindKnowledgeBase)
	group.POST("/knowledge-bases", h.CreateKnowledgeBase)
	group.DELETE("/tenants/:tenant_id/knowledge-bases/:knowledge_base_id", h.DeleteKnowledgeBase)
	// Stable document identity seam (source contract v1): version-fenced
	// upsert, lookup, and tombstone for externally synchronized documents.
	group.PUT("/knowledge-bases/:knowledge_base_id/documents", h.UpsertDocument)
	group.GET("/knowledge-bases/:knowledge_base_id/documents/by-external-id", h.FindDocumentByExternalID)
	group.GET("/knowledge-bases/:knowledge_base_id/documents/:document_id", h.FindDocument)
	group.DELETE("/knowledge-bases/:knowledge_base_id/documents/:document_id", h.DeleteDocument)
}

// RegisterAstaraSystemRoutes omits the sandbox probe and global execution
// administration endpoints from the knowledge-only effective route table.
func RegisterAstaraSystemRoutes(r *gin.RouterGroup, h *handler.SystemHandler, g *rbacGuards, engine *gin.Engine) {
	if h == nil {
		capabilities := deploymentCapabilitiesFromRoutes(engine)
		g.apiKeyRoute(r, http.MethodGet, "/system/capabilities", apiKeyAny(), g.Viewer(), func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"code": 0, "msg": "success", "data": capabilities})
		})
		return
	}
	system := g.apiKeyGroup(r.Group("/system"), apiKeyManageVectorStores(apiKeyFullAccess()))
	system.With(apiKeyAny()).GET("/capabilities", g.Viewer(), h.GetDeploymentCapabilities)
	system.GET("/info", g.Viewer(), h.GetSystemInfo)
	system.GET("/parser-engines", g.Viewer(), h.ListParserEngines)
	system.POST("/parser-engines/check", g.Admin(), h.CheckParserEngines)
	system.POST("/docreader/reconnect", g.Admin(), h.ReconnectDocReader)
	system.GET("/storage-engine-status", g.Viewer(), h.GetStorageEngineStatus)
	system.POST("/storage-engine-check", g.Admin(), h.CheckStorageEngine)
}
