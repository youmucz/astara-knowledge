package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// fakeEmbeddedSessionService is a stand-in for the embedded session service
// used by the cookie-channel tests.
type fakeEmbeddedSessionService struct {
	info   *interfaces.EmbeddedSessionInfo
	err    error
	ttl    time.Duration
	minted string
}

func (f *fakeEmbeddedSessionService) Mint(ctx context.Context, user *types.User, tenantID uint64, permissionRevision string) (string, time.Time, error) {
	f.minted = "minted-token"
	return f.minted, time.Now().Add(f.ttl), nil
}

func (f *fakeEmbeddedSessionService) Validate(ctx context.Context, tokenString string) (*interfaces.EmbeddedSessionInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.info, nil
}

func (f *fakeEmbeddedSessionService) RevokeForUser(ctx context.Context, userID string, tenantID uint64) error {
	return nil
}

func (f *fakeEmbeddedSessionService) SessionTTL() time.Duration {
	if f.ttl == 0 {
		return 30 * time.Minute
	}
	return f.ttl
}

var _ interfaces.EmbeddedSessionService = (*fakeEmbeddedSessionService)(nil)

func embeddedAuthEngine(sessions interfaces.EmbeddedSessionService, tenant *types.Tenant, members *fakeMemberService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(Auth(
		&fakeTenantService{tenant: tenant},
		nil, // userService: never reached on the cookie channel
		members,
		nil, // apiKeyService
		nil, // config
		sessions,
	))
	engine.GET("/api/v1/knowledge-bases", func(c *gin.Context) {
		userID, _ := types.UserIDFromContext(c.Request.Context())
		tenantID, _ := types.TenantIDFromContext(c.Request.Context())
		c.JSON(http.StatusOK, gin.H{
			"user_id":   userID,
			"tenant_id": tenantID,
			"role":      types.TenantRoleFromContext(c.Request.Context()),
			"embedded":  c.Value(types.EmbeddedSessionContextKey.String()) == true,
		})
	})
	return engine
}

func seedRevision(members *fakeMemberService, userID string, tenantID uint64, role types.TenantRole, revision string) {
	members.members[memberKey(userID, tenantID)] = &types.TenantMember{
		UserID:             userID,
		TenantID:           tenantID,
		Role:               role,
		Status:             types.TenantMemberStatusActive,
		PermissionRevision: revision,
	}
}

func TestEmbeddedSessionCookieChannelAuthenticatesTenantPinnedRequests(t *testing.T) {
	tenant := &types.Tenant{ID: 42, Status: "active"}
	members := newFakeMemberService()
	seedRevision(members, "user-1", 42, types.TenantRoleContributor, "rev-9")
	sessions := &fakeEmbeddedSessionService{
		info: &interfaces.EmbeddedSessionInfo{
			User:               &types.User{ID: "user-1", IsActive: true},
			TenantID:           42,
			PermissionRevision: "rev-9",
		},
	}
	engine := embeddedAuthEngine(sessions, tenant, members)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/knowledge-bases", nil)
	req.AddCookie(&http.Cookie{Name: "weknora_embedded_session", Value: "session-token"})
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Body.String(), `"embedded":true`)
	assert.Contains(t, recorder.Body.String(), `"user_id":"user-1"`)
	assert.Contains(t, recorder.Body.String(), `"tenant_id":42`)
	assert.Contains(t, recorder.Body.String(), `"role":"contributor"`)
}

func TestEmbeddedSessionRejectsStalePermissionRevision(t *testing.T) {
	tenant := &types.Tenant{ID: 42, Status: "active"}
	members := newFakeMemberService()
	// Plane bumped the revision (role change); the session still carries rev-1.
	seedRevision(members, "user-1", 42, types.TenantRoleAdmin, "rev-2")
	sessions := &fakeEmbeddedSessionService{
		info: &interfaces.EmbeddedSessionInfo{
			User:               &types.User{ID: "user-1", IsActive: true},
			TenantID:           42,
			PermissionRevision: "rev-1",
		},
	}
	engine := embeddedAuthEngine(sessions, tenant, members)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/knowledge-bases", nil)
	req.AddCookie(&http.Cookie{Name: "weknora_embedded_session", Value: "session-token"})
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "SESSION_EXPIRED")
}

func TestEmbeddedSessionRejectsTenantSwitchHeader(t *testing.T) {
	tenant := &types.Tenant{ID: 42, Status: "active"}
	members := newFakeMemberService()
	seedRevision(members, "user-1", 42, types.TenantRoleViewer, "rev-1")
	sessions := &fakeEmbeddedSessionService{
		info: &interfaces.EmbeddedSessionInfo{
			User:               &types.User{ID: "user-1", IsActive: true},
			TenantID:           42,
			PermissionRevision: "rev-1",
		},
	}
	engine := embeddedAuthEngine(sessions, tenant, members)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/knowledge-bases", nil)
	req.AddCookie(&http.Cookie{Name: "weknora_embedded_session", Value: "session-token"})
	req.Header.Set("X-Tenant-ID", "43")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusForbidden, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "cannot switch workspaces")
}

func TestEmbeddedSessionRejectsInvalidCookieWithSessionExpired(t *testing.T) {
	tenant := &types.Tenant{ID: 42, Status: "active"}
	members := newFakeMemberService()
	sessions := &fakeEmbeddedSessionService{err: errors.New("expired")}
	engine := embeddedAuthEngine(sessions, tenant, members)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/knowledge-bases", nil)
	req.AddCookie(&http.Cookie{Name: "weknora_embedded_session", Value: "tampered"})
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "SESSION_EXPIRED")
}

func TestEmbeddedSessionRejectsRemovedMembership(t *testing.T) {
	tenant := &types.Tenant{ID: 42, Status: "active"}
	members := newFakeMemberService() // no membership rows at all
	sessions := &fakeEmbeddedSessionService{
		info: &interfaces.EmbeddedSessionInfo{
			User:               &types.User{ID: "user-1", IsActive: true},
			TenantID:           42,
			PermissionRevision: "rev-1",
		},
	}
	engine := embeddedAuthEngine(sessions, tenant, members)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/knowledge-bases", nil)
	req.AddCookie(&http.Cookie{Name: "weknora_embedded_session", Value: "session-token"})
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "SESSION_EXPIRED")
}

func TestEmbeddedSessionInactiveTenantFailsClosed(t *testing.T) {
	tenant := &types.Tenant{ID: 42, Status: "suspended"}
	members := newFakeMemberService()
	seedRevision(members, "user-1", 42, types.TenantRoleViewer, "rev-1")
	sessions := &fakeEmbeddedSessionService{
		info: &interfaces.EmbeddedSessionInfo{
			User:               &types.User{ID: "user-1", IsActive: true},
			TenantID:           42,
			PermissionRevision: "rev-1",
		},
	}
	engine := embeddedAuthEngine(sessions, tenant, members)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/knowledge-bases", nil)
	req.AddCookie(&http.Cookie{Name: "weknora_embedded_session", Value: "session-token"})
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
	assert.Contains(t, recorder.Body.String(), "SESSION_EXPIRED")
}
