package handler

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
)

const astaraServiceAuthEnv = "ASTARA_SERVICE_AUTH_SECRET"

type AstaraControlPlaneHandler struct{ db *gorm.DB }

func NewAstaraControlPlaneHandler(db *gorm.DB) *AstaraControlPlaneHandler {
	return &AstaraControlPlaneHandler{db: db}
}

func (h *AstaraControlPlaneHandler) Authenticate(c *gin.Context) {
	secret := strings.TrimSpace(os.Getenv(astaraServiceAuthEnv))
	header := strings.TrimSpace(c.GetHeader("Authorization"))
	provided := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	if secret == "" {
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "service authentication is not configured"})
		return
	}
	if !strings.HasPrefix(header, "Bearer ") || len(provided) != len(secret) ||
		subtle.ConstantTimeCompare([]byte(provided), []byte(secret)) != 1 {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid service authentication"})
		return
	}
	c.Next()
}

type astaraTenantRequest struct {
	ExternalSystem string `json:"external_system" binding:"required"`
	ExternalID     string `json:"external_id" binding:"required"`
	Name           string `json:"name" binding:"required"`
}

type astaraKnowledgeBaseRequest struct {
	TenantID       astaraProviderID `json:"tenant_id" binding:"required"`
	ExternalSystem string           `json:"external_system" binding:"required"`
	ExternalID     string           `json:"external_id" binding:"required"`
	Name           string           `json:"name" binding:"required"`
}

// astaraProviderID accepts the canonical JSON string and a numeric value for
// compatibility with older clients while never accepting floats or objects.
type astaraProviderID string

func (id *astaraProviderID) UnmarshalJSON(raw []byte) error {
	var value string
	if len(raw) > 0 && raw[0] == '"' {
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
	} else {
		value = string(raw)
	}
	if _, err := strconv.ParseUint(value, 10, 64); err != nil {
		return fmt.Errorf("invalid provider ID")
	}
	*id = astaraProviderID(value)
	return nil
}

// astaraProviderResource is intentionally narrower than the native models:
// provider IDs are strings on the cross-repository wire even though WeKnora's
// tenant primary key is numeric.
type astaraProviderResource struct {
	ID             string `json:"id"`
	ExternalSystem string `json:"external_system"`
	ExternalID     string `json:"external_id"`
}

func tenantResource(tenant *types.Tenant) astaraProviderResource {
	resource := astaraProviderResource{ID: strconv.FormatUint(tenant.ID, 10)}
	if tenant.ExternalSystem != nil {
		resource.ExternalSystem = *tenant.ExternalSystem
	}
	if tenant.ExternalID != nil {
		resource.ExternalID = *tenant.ExternalID
	}
	return resource
}

func knowledgeBaseResource(kb *types.KnowledgeBase) astaraProviderResource {
	resource := astaraProviderResource{ID: kb.ID}
	if kb.ExternalSystem != nil {
		resource.ExternalSystem = *kb.ExternalSystem
	}
	if kb.ExternalID != nil {
		resource.ExternalID = *kb.ExternalID
	}
	return resource
}

func normalizedIdentity(system, id string) (string, string, bool) {
	system, id = strings.TrimSpace(system), strings.TrimSpace(id)
	return system, id, system != "" && id != "" && len(system) <= 64 && len(id) <= 255
}

func (h *AstaraControlPlaneHandler) FindTenant(c *gin.Context) {
	system, id, ok := normalizedIdentity(c.Query("external_system"), c.Query("external_id"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid external identity"})
		return
	}
	var tenant types.Tenant
	if err := h.db.WithContext(c).Where("external_system = ? AND external_id = ?", system, id).First(&tenant).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "tenant not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "tenant lookup failed"})
		return
	}
	c.JSON(http.StatusOK, tenantResource(&tenant))
}

func (h *AstaraControlPlaneHandler) CreateTenant(c *gin.Context) {
	var request astaraTenantRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid tenant request"})
		return
	}
	system, id, ok := normalizedIdentity(request.ExternalSystem, request.ExternalID)
	if !ok || strings.TrimSpace(request.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid tenant request"})
		return
	}
	var existing types.Tenant
	err := h.db.WithContext(c).Where("external_system = ? AND external_id = ?", system, id).First(&existing).Error
	if err == nil {
		c.JSON(http.StatusOK, tenantResource(&existing))
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "tenant lookup failed"})
		return
	}
	now := time.Now().UTC()
	tenant := types.Tenant{Name: strings.TrimSpace(request.Name), Status: "active", Business: "astara", ExternalSystem: &system, ExternalID: &id, CreatedAt: now, UpdatedAt: now}
	if err := h.db.WithContext(c).Create(&tenant).Error; err != nil {
		// A concurrent retry can win the unique key race. Re-read and converge.
		if readErr := h.db.WithContext(c).Where("external_system = ? AND external_id = ?", system, id).First(&existing).Error; readErr == nil {
			c.JSON(http.StatusOK, tenantResource(&existing))
			return
		}
		c.JSON(http.StatusConflict, gin.H{"error": "external tenant identity conflict"})
		return
	}
	c.JSON(http.StatusCreated, tenantResource(&tenant))
}

func (h *AstaraControlPlaneHandler) FindKnowledgeBase(c *gin.Context) {
	system, id, ok := normalizedIdentity(c.Query("external_system"), c.Query("external_id"))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid external identity"})
		return
	}
	var kb types.KnowledgeBase
	query := h.db.WithContext(c).Where("external_system = ? AND external_id = ?", system, id)
	if tenantID := strings.TrimSpace(c.Query("tenant_id")); tenantID != "" {
		query = query.Where("tenant_id = ?", tenantID)
	}
	if err := query.First(&kb).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "knowledge base not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "knowledge base lookup failed"})
		return
	}
	c.JSON(http.StatusOK, knowledgeBaseResource(&kb))
}

func (h *AstaraControlPlaneHandler) CreateKnowledgeBase(c *gin.Context) {
	var request astaraKnowledgeBaseRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid knowledge base request"})
		return
	}
	system, id, ok := normalizedIdentity(request.ExternalSystem, request.ExternalID)
	tenantID, parseErr := strconv.ParseUint(string(request.TenantID), 10, 64)
	if !ok || parseErr != nil || tenantID == 0 || strings.TrimSpace(request.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid knowledge base request"})
		return
	}
	var tenant types.Tenant
	if err := h.db.WithContext(c).First(&tenant, tenantID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "tenant not found"})
		return
	}
	var existing types.KnowledgeBase
	err := h.db.WithContext(c).Where("tenant_id = ? AND external_system = ? AND external_id = ?", tenantID, system, id).First(&existing).Error
	if err == nil {
		c.JSON(http.StatusOK, knowledgeBaseResource(&existing))
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "knowledge base lookup failed"})
		return
	}
	now := time.Now().UTC()
	kb := types.KnowledgeBase{ID: uuid.NewString(), Name: strings.TrimSpace(request.Name), Type: types.KnowledgeBaseTypeDocument, TenantID: tenantID, ExternalSystem: &system, ExternalID: &id, CreatedAt: now, UpdatedAt: now}
	if err := h.db.WithContext(c).Create(&kb).Error; err != nil {
		if readErr := h.db.WithContext(c).Where("tenant_id = ? AND external_system = ? AND external_id = ?", tenantID, system, id).First(&existing).Error; readErr == nil {
			c.JSON(http.StatusOK, knowledgeBaseResource(&existing))
			return
		}
		c.JSON(http.StatusConflict, gin.H{"error": "external knowledge base identity conflict"})
		return
	}
	c.JSON(http.StatusCreated, knowledgeBaseResource(&kb))
}

func (h *AstaraControlPlaneHandler) DeleteTenant(c *gin.Context) {
	result := h.db.WithContext(c).Delete(&types.Tenant{}, c.Param("tenant_id"))
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "tenant deletion failed"})
		return
	}
	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "tenant not found"})
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *AstaraControlPlaneHandler) DeleteKnowledgeBase(c *gin.Context) {
	result := h.db.WithContext(c).Where("id = ? AND tenant_id = ?", c.Param("knowledge_base_id"), c.Param("tenant_id")).Delete(&types.KnowledgeBase{})
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "knowledge base deletion failed"})
		return
	}
	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "knowledge base not found"})
		return
	}
	c.Status(http.StatusNoContent)
}
