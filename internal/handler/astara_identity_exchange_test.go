package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const (
	testIdentitySecret = "test-identity-secret"
	testIdentityPrev   = "test-identity-secret-old"
)

type identityTestEnv struct {
	engine  *gin.Engine
	db      *gorm.DB
	session interfaces.EmbeddedSessionService
}

func newIdentityTestEnv(t *testing.T) *identityTestEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv(astaraIdentitySecretEnv, testIdentitySecret)
	t.Setenv(astaraIdentitySecretPreviousEnv, testIdentityPrev)
	t.Setenv(astaraServiceAuthEnv, "test-service-secret")
	t.Setenv("JWT_SECRET", "test-jwt-secret")

	// Unique per-test DSN: cache=shared in-memory databases persist for the
	// whole process, so a shared name would leak tenants between tests.
	dsn := fmt.Sprintf("file:astara-identity-%s?mode=memory&cache=shared", strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&types.Tenant{},
		&types.KnowledgeBase{},
		&types.User{},
		&types.TenantMember{},
		&types.AuthToken{},
	))

	mini := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mini.Addr()})

	userRepo := repository.NewUserRepository(db)
	tokenRepo := repository.NewAuthTokenRepository(db)
	sessionService := service.NewEmbeddedSessionService(userRepo, tokenRepo)
	handlerInstance := NewAstaraIdentityExchangeHandler(db, sessionService, redisClient)

	engine := gin.New()
	group := engine.Group("/api/v1/astara")
	group.POST("/identity/exchange", handlerInstance.Exchange)
	group.POST("/identity/revoke", astaraServiceAuth(), handlerInstance.Revoke)
	return &identityTestEnv{engine: engine, db: db, session: sessionService}
}

func seedExternalTenant(t *testing.T, env *identityTestEnv, tenantID uint64, workspaceID string) *types.Tenant {
	t.Helper()
	externalSystem := astaraExternalSystem
	externalID := workspaceID
	tenant := types.Tenant{
		ID:             tenantID,
		ExternalSystem: &externalSystem,
		ExternalID:     &externalID,
		Name:           "Plane Workspace " + workspaceID,
		Status:         "active",
		Business:       "astara",
	}
	require.NoError(t, env.db.Create(&tenant).Error)
	return &tenant
}

type assertionOptions struct {
	subject      string
	email        string
	workspaceID  string
	tenantID     string
	role         string
	revision     string
	jti          string
	ttl          time.Duration
	audience     string
	issuer       string
	secret       string
	skipIssuedAt bool
}

// mintAssertion produces a Plane-style bootstrap assertion.
func mintAssertion(t *testing.T, options assertionOptions) string {
	t.Helper()
	if options.ttl == 0 {
		options.ttl = 90 * time.Second
	}
	if options.secret == "" {
		options.secret = testIdentitySecret
	}
	if options.audience == "" {
		options.audience = astaraIdentityAudience
	}
	if options.issuer == "" {
		options.issuer = astaraIdentityIssuer
	}
	claims := jwt.MapClaims{
		"aud":    options.audience,
		"iss":    options.issuer,
		"sub":    options.subject,
		"email":  options.email,
		"name":   "Plane Member",
		"ws":     options.workspaceID,
		"tenant": options.tenantID,
		"role":   options.role,
		"caps":   []string{"knowledge-bases", "search"},
		"rev":    options.revision,
		"jti":    options.jti,
		"exp":    time.Now().Add(options.ttl).Unix(),
	}
	if !options.skipIssuedAt {
		claims["iat"] = time.Now().Unix()
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(options.secret))
	require.NoError(t, err)
	return signed
}

func postExchange(t *testing.T, env *identityTestEnv, assertion string) *httptest.ResponseRecorder {
	t.Helper()
	return postJSON(t, env, http.MethodPost, "/api/v1/astara/identity/exchange",
		map[string]string{"assertion": assertion}, "")
}

func postJSON(t *testing.T, env *identityTestEnv, method, path string, body any, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	encoded, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(method, path, bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	recorder := httptest.NewRecorder()
	env.engine.ServeHTTP(recorder, req)
	return recorder
}

func sessionCookieFrom(t *testing.T, recorder *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	// Set-Cookie is a response header: parse it through http.Response
	// instead of Request.Cookies (which reads the request Cookie header).
	header := http.Header{}
	for _, raw := range recorder.Header().Values("Set-Cookie") {
		header.Add("Set-Cookie", raw)
	}
	cookies := (&http.Response{Header: header}).Cookies()
	require.NotEmpty(t, cookies)
	return cookies[0]
}

func TestExchangeHappyPathProvisionsShadowIdentityAndSession(t *testing.T) {
	env := newIdentityTestEnv(t)
	seedExternalTenant(t, env, 7, "ws-1")

	recorder := postExchange(t, env, mintAssertion(t, assertionOptions{
		subject: "plane-user-1", email: "member@plane.test",
		workspaceID: "ws-1", tenantID: "7", role: "contributor", revision: "rev-1", jti: "jti-1",
	}))
	cookie := sessionCookieFrom(t, recorder)

	// HttpOnly session cookie bound to the same-origin proxy path.
	assert.True(t, cookie.HttpOnly)
	assert.Equal(t, "/api/knowledge", cookie.Path)

	// Shadow user + membership repaired.
	var user types.User
	require.NoError(t, env.db.Where("external_system = ? AND external_id = ?", "astara", "plane-user-1").First(&user).Error)
	assert.Equal(t, "member@plane.test", user.Email)
	assert.True(t, user.IsActive)
	var member types.TenantMember
	require.NoError(t, env.db.Where("user_id = ? AND tenant_id = ?", user.ID, 7).First(&member).Error)
	assert.Equal(t, types.TenantRoleContributor, member.Role)
	assert.Equal(t, types.TenantMemberStatusActive, member.Status)
	assert.Equal(t, "rev-1", member.PermissionRevision)

	// The session validates and carries the revision binding.
	info, err := env.session.Validate(context.Background(), cookie.Value)
	require.NoError(t, err)
	assert.Equal(t, user.ID, info.User.ID)
	assert.Equal(t, uint64(7), info.TenantID)
	assert.Equal(t, "rev-1", info.PermissionRevision)
}

func TestExchangeIsOneTime(t *testing.T) {
	env := newIdentityTestEnv(t)
	seedExternalTenant(t, env, 7, "ws-1")
	assertion := mintAssertion(t, assertionOptions{
		subject: "plane-user-1", email: "member@plane.test",
		workspaceID: "ws-1", tenantID: "7", role: "viewer", revision: "rev-1", jti: "jti-once",
	})
	first := postExchange(t, env, assertion)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	second := postExchange(t, env, assertion)
	assert.Equal(t, http.StatusUnauthorized, second.Code)
}

func TestExchangeRejectsInvalidAssertions(t *testing.T) {
	env := newIdentityTestEnv(t)
	seedExternalTenant(t, env, 7, "ws-1")

	cases := []struct {
		name      string
		assertion string
	}{
		{"wrong secret", mintAssertion(t, assertionOptions{
			subject: "u", email: "e@x.test", workspaceID: "ws-1", tenantID: "7",
			role: "viewer", revision: "r", jti: "j-1", secret: "attacker-secret",
		})},
		{"wrong audience", mintAssertion(t, assertionOptions{
			subject: "u", email: "e@x.test", workspaceID: "ws-1", tenantID: "7",
			role: "viewer", revision: "r", jti: "j-2", audience: "other-audience",
		})},
		{"wrong issuer", mintAssertion(t, assertionOptions{
			subject: "u", email: "e@x.test", workspaceID: "ws-1", tenantID: "7",
			role: "viewer", revision: "r", jti: "j-3", issuer: "attacker",
		})},
		{"expired", mintAssertion(t, assertionOptions{
			subject: "u", email: "e@x.test", workspaceID: "ws-1", tenantID: "7",
			role: "viewer", revision: "r", jti: "j-4", ttl: -time.Minute,
		})},
		{"excessive lifetime", mintAssertion(t, assertionOptions{
			subject: "u", email: "e@x.test", workspaceID: "ws-1", tenantID: "7",
			role: "viewer", revision: "r", jti: "j-5", ttl: time.Hour,
		})},
		{"forged role", mintAssertion(t, assertionOptions{
			subject: "u", email: "e@x.test", workspaceID: "ws-1", tenantID: "7",
			role: "owner", revision: "r", jti: "j-6",
		})},
		{"missing jti", mintAssertion(t, assertionOptions{
			subject: "u", email: "e@x.test", workspaceID: "ws-1", tenantID: "7",
			role: "viewer", revision: "r",
		})},
		{"tenant remap", mintAssertion(t, assertionOptions{
			subject: "u", email: "e@x.test", workspaceID: "ws-OTHER", tenantID: "7",
			role: "viewer", revision: "r", jti: "j-7",
		})},
		{"unknown tenant", mintAssertion(t, assertionOptions{
			subject: "u", email: "e@x.test", workspaceID: "ws-1", tenantID: "999",
			role: "viewer", revision: "r", jti: "j-8",
		})},
		{"missing iat", mintAssertion(t, assertionOptions{
			subject: "u", email: "e@x.test", workspaceID: "ws-1", tenantID: "7",
			role: "viewer", revision: "r", jti: "j-9", skipIssuedAt: true,
		})},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := postExchange(t, env, testCase.assertion)
			assert.Equal(t, http.StatusUnauthorized, recorder.Code, recorder.Body.String())
		})
	}
	// No shadow identity may leak from the rejected exchanges.
	var count int64
	require.NoError(t, env.db.Model(&types.User{}).Where("external_system = ?", "astara").Count(&count).Error)
	assert.Equal(t, int64(0), count)
}

func TestExchangeRejectsNativeEmailConflict(t *testing.T) {
	env := newIdentityTestEnv(t)
	seedExternalTenant(t, env, 7, "ws-1")
	native := types.User{ID: "native-1", Username: "native", Email: "member@plane.test", PasswordHash: "x", TenantID: 7, IsActive: true}
	require.NoError(t, env.db.Create(&native).Error)

	recorder := postExchange(t, env, mintAssertion(t, assertionOptions{
		subject: "plane-user-1", email: "member@plane.test",
		workspaceID: "ws-1", tenantID: "7", role: "viewer", revision: "rev-1", jti: "jti-conflict",
	}))
	assert.Equal(t, http.StatusConflict, recorder.Code)
	// The native account was not taken over.
	var user types.User
	require.NoError(t, env.db.First(&user, "id = ?", "native-1").Error)
	assert.Nil(t, user.ExternalID)
}

func TestExchangeRepairsRoleAndRevisionAndInvalidatesStaleSessions(t *testing.T) {
	env := newIdentityTestEnv(t)
	seedExternalTenant(t, env, 7, "ws-1")

	first := postExchange(t, env, mintAssertion(t, assertionOptions{
		subject: "plane-user-1", email: "member@plane.test",
		workspaceID: "ws-1", tenantID: "7", role: "viewer", revision: "rev-1", jti: "jti-a",
	}))
	staleCookie := sessionCookieFrom(t, first)

	// Plane promotes the member and bumps the permission revision.
	second := postExchange(t, env, mintAssertion(t, assertionOptions{
		subject: "plane-user-1", email: "member@plane.test",
		workspaceID: "ws-1", tenantID: "7", role: "admin", revision: "rev-2", jti: "jti-b",
	}))
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())

	var user types.User
	require.NoError(t, env.db.Where("external_system = ? AND external_id = ?", "astara", "plane-user-1").First(&user).Error)
	var member types.TenantMember
	require.NoError(t, env.db.Where("user_id = ? AND tenant_id = ?", user.ID, 7).First(&member).Error)
	assert.Equal(t, types.TenantRoleAdmin, member.Role)
	assert.Equal(t, "rev-2", member.PermissionRevision)

	// The earlier session (rev-1) was revoked by the fresh exchange.
	_, err := env.session.Validate(context.Background(), staleCookie.Value)
	assert.Error(t, err)
}

func TestExchangeAcceptsRotatedSecret(t *testing.T) {
	env := newIdentityTestEnv(t)
	seedExternalTenant(t, env, 7, "ws-1")
	// Assertion signed with the previous secret stays valid during rotation.
	recorder := postExchange(t, env, mintAssertion(t, assertionOptions{
		subject: "plane-user-1", email: "member@plane.test",
		workspaceID: "ws-1", tenantID: "7", role: "viewer", revision: "rev-1", jti: "jti-rot",
		secret: testIdentityPrev,
	}))
	assert.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
}

func TestRevokeEndpointInvalidatesEmbeddedSessions(t *testing.T) {
	env := newIdentityTestEnv(t)
	seedExternalTenant(t, env, 7, "ws-1")
	exchanged := postExchange(t, env, mintAssertion(t, assertionOptions{
		subject: "plane-user-1", email: "member@plane.test",
		workspaceID: "ws-1", tenantID: "7", role: "viewer", revision: "rev-1", jti: "jti-rev",
	}))
	cookie := sessionCookieFrom(t, exchanged)

	// Service auth is required.
	unauthorized := postJSON(t, env, http.MethodPost, "/api/v1/astara/identity/revoke",
		map[string]string{"external_user_id": "plane-user-1"}, "")
	assert.Equal(t, http.StatusUnauthorized, unauthorized.Code)

	// Wrong secret is rejected.
	wrong := postJSON(t, env, http.MethodPost, "/api/v1/astara/identity/revoke",
		map[string]string{"external_user_id": "plane-user-1"}, "Bearer wrong-service-secret")
	assert.Equal(t, http.StatusUnauthorized, wrong.Code)

	revoked := postJSON(t, env, http.MethodPost, "/api/v1/astara/identity/revoke",
		map[string]string{"external_user_id": "plane-user-1"}, "Bearer test-service-secret")
	assert.Equal(t, http.StatusNoContent, revoked.Code)

	_, err := env.session.Validate(context.Background(), cookie.Value)
	assert.Error(t, err)

	// Revoking an unknown identity stays idempotent (204).
	unknown := postJSON(t, env, http.MethodPost, "/api/v1/astara/identity/revoke",
		map[string]string{"external_user_id": "plane-user-unknown"}, "Bearer test-service-secret")
	assert.Equal(t, http.StatusNoContent, unknown.Code)
}

func TestExchangeFailClosedWithoutReplayDefense(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv(astaraIdentitySecretEnv, testIdentitySecret)
	t.Setenv("JWT_SECRET", "test-jwt-secret")
	db, err := gorm.Open(sqlite.Open("file:astara-identity-noreplay-1?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.Tenant{}, &types.User{}, &types.TenantMember{}, &types.AuthToken{}, &types.KnowledgeBase{}))
	externalSystem, externalID := astaraExternalSystem, "ws-1"
	require.NoError(t, db.Create(&types.Tenant{ID: 7, ExternalSystem: &externalSystem, ExternalID: &externalID, Name: "T", Status: "active"}).Error)

	sessions := service.NewEmbeddedSessionService(repository.NewUserRepository(db), repository.NewAuthTokenRepository(db))
	handlerInstance := NewAstaraIdentityExchangeHandler(db, sessions, nil)
	engine := gin.New()
	engine.POST("/api/v1/astara/identity/exchange", handlerInstance.Exchange)

	recorder := postExchange(t, &identityTestEnv{engine: engine}, mintAssertion(t, assertionOptions{
		subject: "u", email: "e@x.test", workspaceID: "ws-1", tenantID: "7",
		role: "viewer", revision: "r", jti: "j-noreplay",
	}))
	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func TestEmbeddedSessionTokenCarriesCookieOnlyType(t *testing.T) {
	env := newIdentityTestEnv(t)
	seedExternalTenant(t, env, 7, "ws-1")
	exchanged := postExchange(t, env, mintAssertion(t, assertionOptions{
		subject: "plane-user-1", email: "member@plane.test",
		workspaceID: "ws-1", tenantID: "7", role: "viewer", revision: "rev-1", jti: "jti-bearer",
	}))
	cookie := sessionCookieFrom(t, exchanged)

	// The embedded session cookie value carries the embedded-session type
	// claim so the bearer-validation path refuses to accept it as a login
	// token (see user service ValidateToken).
	claims := jwt.MapClaims{}
	_, _, err := jwt.NewParser().ParseUnverified(cookie.Value, claims)
	require.NoError(t, err)
	assert.Equal(t, service.EmbeddedSessionClaimType, claims["type"])
}
