package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// Embedded session constants shared with the auth middleware and the
// identity exchange handler.
const (
	// EmbeddedSessionTokenType is the AuthToken row type for embedded
	// sessions; the bearer-token path rejects it and the cookie path
	// requires it.
	EmbeddedSessionTokenType = "embedded_session"
	// EmbeddedSessionClaimType must be present in every embedded session
	// JWT so an embedded cookie can never be replayed as a login bearer
	// token (and vice versa).
	EmbeddedSessionClaimType = "embedded_session"
)

const (
	embeddedSessionTTLEnv     = "ASTARA_EMBEDDED_SESSION_TTL"
	defaultEmbeddedSessionTTL = 30 * time.Minute
	minEmbeddedSessionTTL     = time.Minute
	maxEmbeddedSessionTTL     = 4 * time.Hour
)

// embeddedSessionTTL parses the configured TTL with fail-closed bounds.
func embeddedSessionTTL() time.Duration {
	raw := os.Getenv(embeddedSessionTTLEnv)
	if raw == "" {
		return defaultEmbeddedSessionTTL
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed < minEmbeddedSessionTTL || parsed > maxEmbeddedSessionTTL {
		return defaultEmbeddedSessionTTL
	}
	return parsed
}

type embeddedSessionService struct {
	userRepo  interfaces.UserRepository
	tokenRepo interfaces.AuthTokenRepository
}

// NewEmbeddedSessionService builds the embedded session service.
func NewEmbeddedSessionService(
	userRepo interfaces.UserRepository,
	tokenRepo interfaces.AuthTokenRepository,
) interfaces.EmbeddedSessionService {
	return &embeddedSessionService{userRepo: userRepo, tokenRepo: tokenRepo}
}

func (s *embeddedSessionService) SessionTTL() time.Duration {
	return embeddedSessionTTL()
}

func (s *embeddedSessionService) Mint(
	ctx context.Context,
	user *types.User,
	tenantID uint64,
	permissionRevision string,
) (string, time.Time, error) {
	if user == nil || user.ID == "" || tenantID == 0 {
		return "", time.Time{}, errors.New("embedded session requires user and tenant")
	}
	ttl := embeddedSessionTTL()
	expiresAt := time.Now().Add(ttl)
	claims := jwt.MapClaims{
		"user_id":            user.ID,
		"tenant_id":          tenantID,
		"permission_rev":     permissionRevision,
		"type":               EmbeddedSessionClaimType,
		"iat":                time.Now().Unix(),
		"exp":                expiresAt.Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(getJwtSecret()))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign embedded session: %w", err)
	}
	record := &types.AuthToken{
		ID:        uuid.New().String(),
		UserID:    user.ID,
		Token:     signed,
		TokenType: EmbeddedSessionTokenType,
		ExpiresAt: expiresAt,
	}
	if err := s.tokenRepo.CreateToken(ctx, record); err != nil {
		return "", time.Time{}, fmt.Errorf("persist embedded session: %w", err)
	}
	return signed, expiresAt, nil
}

func (s *embeddedSessionService) Validate(ctx context.Context, tokenString string) (*interfaces.EmbeddedSessionInfo, error) {
	parsed, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(getJwtSecret()), nil
	})
	if err != nil || !parsed.Valid {
		return nil, errors.New("invalid embedded session token")
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok || claims["type"] != EmbeddedSessionClaimType {
		return nil, errors.New("not an embedded session token")
	}
	userID, _ := claims["user_id"].(string)
	if userID == "" {
		return nil, errors.New("embedded session token missing user")
	}
	tenantID := uint64(0)
	switch value := claims["tenant_id"].(type) {
	case float64:
		tenantID = uint64(value)
	case string:
		parsed, parseErr := strconv.ParseUint(value, 10, 64)
		if parseErr != nil {
			return nil, errors.New("embedded session token has invalid tenant")
		}
		tenantID = parsed
	default:
		return nil, errors.New("embedded session token missing tenant")
	}
	if tenantID == 0 {
		return nil, errors.New("embedded session token missing tenant")
	}
	permissionRevision, _ := claims["permission_rev"].(string)

	record, err := s.tokenRepo.GetTokenByValue(ctx, tokenString)
	if err != nil || record == nil || record.IsRevoked {
		return nil, errors.New("embedded session is revoked")
	}
	if record.TokenType != EmbeddedSessionTokenType {
		return nil, errors.New("token type mismatch")
	}
	if record.ExpiresAt.Before(time.Now()) {
		return nil, errors.New("embedded session is expired")
	}

	user, err := s.userRepo.GetUserByID(ctx, userID)
	if err != nil || user == nil || !user.IsActive {
		return nil, errors.New("embedded session user is not active")
	}
	return &interfaces.EmbeddedSessionInfo{
		User:               user,
		TenantID:           tenantID,
		PermissionRevision: permissionRevision,
	}, nil
}

func (s *embeddedSessionService) RevokeForUser(ctx context.Context, userID string, tenantID uint64) error {
	tokens, err := s.tokenRepo.GetTokensByUserID(ctx, userID)
	if err != nil {
		return err
	}
	for _, token := range tokens {
		if token == nil || token.TokenType != EmbeddedSessionTokenType || token.IsRevoked {
			continue
		}
		if tenantID != 0 {
			// The AuthToken row does not carry the tenant; the caller that
			// needs tenant-scoped revocation passes the token values it
			// observed. Here we rely on the token's own claims.
			info, err := s.Validate(ctx, token.Token)
			if err != nil {
				continue
			}
			if info.TenantID != tenantID {
				continue
			}
		}
		token.IsRevoked = true
		if err := s.tokenRepo.UpdateToken(ctx, token); err != nil {
			return err
		}
	}
	return nil
}
