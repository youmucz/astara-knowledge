package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// EmbeddedSessionInfo is the validated projection of an embedded session
// cookie. The auth middleware resolves the tenant role and compares the
// permission revision against the current membership state.
type EmbeddedSessionInfo struct {
	User               *types.User
	TenantID           uint64
	PermissionRevision string
}

// EmbeddedSessionService mints and validates the short-lived sessions used
// by the Plane-hosted embedded surface. Sessions are HttpOnly cookies —
// never handed to JavaScript — and are stored as AuthToken rows so logout
// and revocation flows cover them like any other token.
type EmbeddedSessionService interface {
	// Mint issues a new embedded session bound to user/tenant/revision.
	Mint(ctx context.Context, user *types.User, tenantID uint64, permissionRevision string) (token string, expiresAt time.Time, err error)
	// Validate parses and checks an embedded session token (signature,
	// type, revocation, expiry, user). Membership/revision checks are the
	// caller's responsibility.
	Validate(ctx context.Context, tokenString string) (*EmbeddedSessionInfo, error)
	// RevokeForUser revokes embedded sessions of the user. tenantID == 0
	// revokes across every tenant.
	RevokeForUser(ctx context.Context, userID string, tenantID uint64) error
	// SessionTTL is how long a freshly minted session stays valid.
	SessionTTL() time.Duration
}
