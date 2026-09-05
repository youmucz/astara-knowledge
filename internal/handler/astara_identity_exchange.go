package handler

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// Embedded identity exchange: the Plane backend mints a one-time signed
// assertion for an authenticated member; this private endpoint verifies it,
// repairs the shadow identity, and issues a short-lived HttpOnly session.
//
// Wire contract (UI/source contract 1):
//
//	POST /api/v1/astara/identity/exchange  {"assertion": "<JWT>"}   (assertion-authenticated)
//	POST /api/v1/astara/identity/revoke    {"external_user_id": "<uuid>", "tenant_id": "<id>?"}  (service-authenticated)
const (
	astaraIdentitySecretEnv         = "ASTARA_IDENTITY_EXCHANGE_SECRET"
	astaraIdentitySecretPreviousEnv = "ASTARA_IDENTITY_EXCHANGE_SECRET_PREVIOUS"
	astaraIdentityAudience          = "astara-knowledge-identity-exchange"
	astaraIdentityIssuer            = "astara-plane"
	astaraExternalSystem            = "astara"
	// Assertion lifetime ceiling: a valid assertion may never outlive this
	// window regardless of the exp claim the issuer set.
	astaraAssertionMaxLifetime = 5 * time.Minute
	// Cookie wiring for the same-origin Plane proxy prefix.
	astaraSessionCookieEnv         = "ASTARA_EMBEDDED_SESSION_COOKIE"
	astaraSessionCookieDefault     = "weknora_embedded_session"
	astaraSessionCookiePathEnv     = "ASTARA_EMBEDDED_SESSION_COOKIE_PATH"
	astaraSessionCookiePathDefault = "/api/knowledge"
)

type AstaraIdentityExchangeHandler struct {
	db       *gorm.DB
	sessions interfaces.EmbeddedSessionService
	redis    *redis.Client
}

func NewAstaraIdentityExchangeHandler(
	db *gorm.DB,
	sessions interfaces.EmbeddedSessionService,
	redisClient *redis.Client,
) *AstaraIdentityExchangeHandler {
	return &AstaraIdentityExchangeHandler{db: db, sessions: sessions, redis: redisClient}
}

// identityAssertion is the closed claim set of the Plane bootstrap
// assertion. Unknown claims are ignored; every field below is required.
type identityAssertion struct {
	Subject            string
	Email              string
	Name               string
	WorkspaceID        string
	ProviderTenantID   string
	Role               string
	Capabilities       []string
	PermissionRevision string
	JTI                string
}

type exchangeRequest struct {
	Assertion string `json:"assertion" binding:"required"`
}

type revokeRequest struct {
	ExternalUserID string `json:"external_user_id" binding:"required"`
	TenantID       string `json:"tenant_id"`
}

func identitySecrets() (string, string) {
	return strings.TrimSpace(os.Getenv(astaraIdentitySecretEnv)),
		strings.TrimSpace(os.Getenv(astaraIdentitySecretPreviousEnv))
}

// verifyAssertion validates signature, audience, expiry and bounded
// lifetime, and returns the closed claim set plus the assertion expiry
// (used to size the replay-defense TTL). Every failure maps to the same
// wire error so callers cannot probe provider state.
func (h *AstaraIdentityExchangeHandler) verifyAssertion(tokenString string) (*identityAssertion, time.Time, error) {
	current, previous := identitySecrets()
	if current == "" {
		return nil, time.Time{}, errors.New("identity exchange is not configured")
	}
	secrets := []string{current}
	if previous != "" && previous != current {
		secrets = append(secrets, previous)
	}

	var parsed *jwt.Token
	var parseErr error
	for _, secret := range secrets {
		token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, errors.New("unexpected signing method")
			}
			return []byte(secret), nil
		}, jwt.WithExpirationRequired(), jwt.WithAudience(astaraIdentityAudience), jwt.WithIssuer(astaraIdentityIssuer))
		if err == nil && token.Valid {
			parsed = token
			parseErr = nil
			break
		}
		parseErr = err
	}
	if parsed == nil || parseErr != nil {
		return nil, time.Time{}, errors.New("invalid assertion")
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return nil, time.Time{}, errors.New("invalid assertion")
	}

	assertion := &identityAssertion{}
	assertion.Subject, _ = claims["sub"].(string)
	assertion.Email, _ = claims["email"].(string)
	assertion.Name, _ = claims["name"].(string)
	assertion.WorkspaceID, _ = claims["ws"].(string)
	assertion.ProviderTenantID, _ = claims["tenant"].(string)
	assertion.Role, _ = claims["role"].(string)
	assertion.PermissionRevision, _ = claims["rev"].(string)
	assertion.JTI, _ = claims["jti"].(string)
	if rawCaps, ok := claims["caps"].([]interface{}); ok {
		for _, entry := range rawCaps {
			if value, ok := entry.(string); ok && value != "" {
				assertion.Capabilities = append(assertion.Capabilities, value)
			}
		}
	}

	if assertion.Subject == "" || len(assertion.Subject) > 255 ||
		assertion.Email == "" || len(assertion.Email) > 255 ||
		len(assertion.Name) > 255 ||
		assertion.WorkspaceID == "" || len(assertion.WorkspaceID) > 255 ||
		assertion.ProviderTenantID == "" ||
		assertion.PermissionRevision == "" || len(assertion.PermissionRevision) > 64 ||
		assertion.JTI == "" || len(assertion.JTI) > 128 {
		return nil, time.Time{}, errors.New("assertion claims incomplete")
	}
	switch assertion.Role {
	case string(types.TenantRoleViewer), string(types.TenantRoleContributor), string(types.TenantRoleAdmin):
	default:
		return nil, time.Time{}, errors.New("assertion role is not permitted")
	}
	if _, err := strconv.ParseUint(assertion.ProviderTenantID, 10, 64); err != nil {
		return nil, time.Time{}, errors.New("assertion tenant is invalid")
	}
	if len(assertion.Capabilities) > 64 {
		return nil, time.Time{}, errors.New("assertion capabilities are invalid")
	}

	// Bounded lifetime: reject assertions whose total validity window
	// exceeds the contract ceiling (or whose iat is missing).
	issuedAt, err := claims.GetIssuedAt()
	if err != nil || issuedAt == nil {
		return nil, time.Time{}, errors.New("assertion claims incomplete")
	}
	expiresAt, err := claims.GetExpirationTime()
	if err != nil || expiresAt == nil {
		return nil, time.Time{}, errors.New("assertion claims incomplete")
	}
	if expiresAt.Sub(issuedAt.Time) > astaraAssertionMaxLifetime {
		return nil, time.Time{}, errors.New("assertion lifetime exceeds the allowed window")
	}
	return assertion, expiresAt.Time, nil
}

// consumeJTI enforces one-time use: the jti is written with a TTL that
// outlives the assertion. Fails closed when the store is unavailable.
func (h *AstaraIdentityExchangeHandler) consumeJTI(c *gin.Context, assertion *identityAssertion, expiresAt time.Time) error {
	if h.redis == nil {
		return errors.New("replay defense is unavailable")
	}
	ttl := time.Until(expiresAt) + time.Minute
	if ttl <= 0 {
		return errors.New("assertion is expired")
	}
	key := "astara-knowledge:identity-jti:" + assertion.JTI
	ok, err := h.redis.SetNX(c.Request.Context(), key, "1", ttl).Result()
	if err != nil {
		return errors.New("replay defense is unavailable")
	}
	if !ok {
		return errors.New("assertion was already used")
	}
	return nil
}

// resolveTenant maps the assertion onto exactly one provisioned tenant and
// rejects remapping attempts (assertion for workspace A vs tenant B).
func (h *AstaraIdentityExchangeHandler) resolveTenant(c *gin.Context, assertion *identityAssertion) (*types.Tenant, error) {
	tenantID, _ := strconv.ParseUint(assertion.ProviderTenantID, 10, 64)
	var tenant types.Tenant
	if err := h.db.WithContext(c).First(&tenant, tenantID).Error; err != nil {
		return nil, errors.New("tenant is not available")
	}
	if tenant.ExternalSystem == nil || tenant.ExternalID == nil ||
		*tenant.ExternalSystem != astaraExternalSystem || *tenant.ExternalID != assertion.WorkspaceID {
		return nil, errors.New("tenant identity mismatch")
	}
	if tenant.Status != "active" {
		return nil, errors.New("tenant is not available")
	}
	return &tenant, nil
}

// ensureShadowUser finds or provisions the shadow user for the Plane
// identity. Native accounts with a colliding email are never taken over.
func (h *AstaraIdentityExchangeHandler) ensureShadowUser(c *gin.Context, assertion *identityAssertion, tenant *types.Tenant) (*types.User, error) {
	var user types.User
	err := h.db.WithContext(c).Where("external_system = ? AND external_id = ?", astaraExternalSystem, assertion.Subject).First(&user).Error
	if err == nil {
		return &user, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errors.New("shadow user lookup failed")
	}

	// Email conflict: a native WeKnora account already owns this address.
	// Fail closed — the shadow identity must not hijack it.
	var existing types.User
	if err := h.db.WithContext(c).Where("email = ?", assertion.Email).First(&existing).Error; err == nil {
		return nil, errors.New("identity conflict")
	}

	randomBytes := make([]byte, 24)
	if _, err := rand.Read(randomBytes); err != nil {
		return nil, errors.New("shadow user provisioning failed")
	}
	unusablePassword := "astara-embedded:" + base64.StdEncoding.EncodeToString(randomBytes)
	user = types.User{
		ID:             uuid.New().String(),
		ExternalSystem: strPtr(astaraExternalSystem),
		ExternalID:     strPtr(assertion.Subject),
		Username:       embeddedUsername(assertion.Subject),
		Email:          assertion.Email,
		PasswordHash:   unusablePassword,
		TenantID:       tenant.ID,
		IsActive:       true,
	}
	if err := h.db.WithContext(c).Create(&user).Error; err != nil {
		// A concurrent exchange can win the unique race; converge onto it.
		var winner types.User
		if readErr := h.db.WithContext(c).Where("external_system = ? AND external_id = ?", astaraExternalSystem, assertion.Subject).First(&winner).Error; readErr == nil {
			return &winner, nil
		}
		return nil, errors.New("shadow user provisioning failed")
	}
	return &user, nil
}

// embeddedUsername derives a stable, non-guessable username for a shadow
// user. It is display-only: shadow users never log in directly.
func embeddedUsername(subject string) string {
	digest := hex.EncodeToString([]byte(subject))
	if len(digest) > 16 {
		digest = digest[:16]
	}
	return "plane-" + digest
}

// repairMembership creates or repairs the tenant membership with the
// assertion's role and permission revision. Plane stays authoritative.
func (h *AstaraIdentityExchangeHandler) repairMembership(c *gin.Context, user *types.User, tenant *types.Tenant, assertion *identityAssertion) error {
	var member types.TenantMember
	err := h.db.WithContext(c).Where("user_id = ? AND tenant_id = ?", user.ID, tenant.ID).First(&member).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		member = types.TenantMember{
			UserID:             user.ID,
			TenantID:           tenant.ID,
			Role:               types.TenantRole(assertion.Role),
			Status:             types.TenantMemberStatusActive,
			PermissionRevision: assertion.PermissionRevision,
			JoinedAt:           time.Now().UTC(),
		}
		return h.db.WithContext(c).Create(&member).Error
	}
	if err != nil {
		return err
	}
	if member.Role != types.TenantRole(assertion.Role) ||
		member.Status != types.TenantMemberStatusActive ||
		member.PermissionRevision != assertion.PermissionRevision {
		member.Role = types.TenantRole(assertion.Role)
		member.Status = types.TenantMemberStatusActive
		member.PermissionRevision = assertion.PermissionRevision
		return h.db.WithContext(c).Model(&types.TenantMember{}).Where("id = ?", member.ID).Updates(map[string]interface{}{
			"role":                member.Role,
			"status":              member.Status,
			"permission_revision": member.PermissionRevision,
		}).Error
	}
	return nil
}

func sessionCookieName() string {
	if value := strings.TrimSpace(os.Getenv(astaraSessionCookieEnv)); value != "" {
		return value
	}
	return astaraSessionCookieDefault
}

func sessionCookiePath() string {
	if value := strings.TrimSpace(os.Getenv(astaraSessionCookiePathEnv)); value != "" {
		return value
	}
	return astaraSessionCookiePathDefault
}

func isTLS(c *gin.Context) bool {
	return c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https")
}

// Exchange implements POST /api/v1/astara/identity/exchange.
func (h *AstaraIdentityExchangeHandler) Exchange(c *gin.Context) {
	var request exchangeRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid exchange request"})
		return
	}
	assertion, assertionExpiresAt, err := h.verifyAssertion(request.Assertion)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid assertion"})
		return
	}
	if err := h.consumeJTI(c, assertion, assertionExpiresAt); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid assertion"})
		return
	}

	tenant, err := h.resolveTenant(c, assertion)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid assertion"})
		return
	}
	user, err := h.ensureShadowUser(c, assertion, tenant)
	if err != nil {
		logger.Warnf(c.Request.Context(), "[astara-identity] shadow user provisioning failed: %v", err)
		c.JSON(http.StatusConflict, gin.H{"error": "identity conflict"})
		return
	}
	if err := h.repairMembership(c, user, tenant, assertion); err != nil {
		logger.Warnf(c.Request.Context(), "[astara-identity] membership repair failed for user %s: %v", user.ID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "identity exchange failed"})
		return
	}

	// Workspace-switch hygiene: a fresh exchange invalidates every earlier
	// embedded session of this user, so a session can never outlive its
	// Workspace binding.
	if err := h.sessions.RevokeForUser(c.Request.Context(), user.ID, 0); err != nil {
		logger.Warnf(c.Request.Context(), "[astara-identity] prior session revocation failed for user %s: %v", user.ID, err)
	}

	token, sessionExpiresAt, err := h.sessions.Mint(c.Request.Context(), user, tenant.ID, assertion.PermissionRevision)
	if err != nil {
		logger.Warnf(c.Request.Context(), "[astara-identity] session mint failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "identity exchange failed"})
		return
	}

	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(sessionCookieName(), token, int(h.sessions.SessionTTL().Seconds()), sessionCookiePath(), "", isTLS(c), true)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"expires_at": sessionExpiresAt.UTC().Format(time.RFC3339),
		},
	})
}

// Revoke implements POST /api/v1/astara/identity/revoke (service auth).
func (h *AstaraIdentityExchangeHandler) Revoke(c *gin.Context) {
	var request revokeRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid revoke request"})
		return
	}
	tenantID := uint64(0)
	if request.TenantID != "" {
		parsed, err := strconv.ParseUint(request.TenantID, 10, 64)
		if err != nil || parsed == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid revoke request"})
			return
		}
		tenantID = parsed
	}
	var user types.User
	err := h.db.WithContext(c).Where("external_system = ? AND external_id = ?", astaraExternalSystem, request.ExternalUserID).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Unknown shadow identity: nothing to revoke. 204 keeps the caller
		// idempotent without revealing whether the identity exists.
		c.Status(http.StatusNoContent)
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "revocation failed"})
		return
	}
	if err := h.sessions.RevokeForUser(c.Request.Context(), user.ID, tenantID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "revocation failed"})
		return
	}
	c.Status(http.StatusNoContent)
}

func strPtr(value string) *string {
	return &value
}
