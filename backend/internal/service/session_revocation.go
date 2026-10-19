package service

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// SessionRevocationCache makes access tokens stop working before they expire:
// a revoked session id (logout, a revoked refresh family) and a per-user
// "valid after" point (revoke all sessions). The Redis refresh token cache
// implements it.
type SessionRevocationCache interface {
	RevokeSession(ctx context.Context, sessionID string, ttl time.Duration) error
	SetUserTokensValidAfter(ctx context.Context, userID int64, validAfter time.Time, ttl time.Duration) error
	// GetSessionRevocation reports whether sessionID is revoked and the user's
	// valid-after point (zero when none), in one round trip.
	GetSessionRevocation(ctx context.Context, sessionID string, userID int64) (revoked bool, validAfter time.Time, err error)
}

const revocationCheckFailLogInterval = 30 * time.Second

var lastRevocationCheckFailLog atomic.Int64

func (s *AuthService) sessionRevocation() (SessionRevocationCache, bool) {
	if s.refreshTokenCache == nil {
		return nil, false
	}
	cache, ok := s.refreshTokenCache.(SessionRevocationCache)
	return cache, ok
}

// revocationTTL covers the longest access token that may still be live: the
// configured lifetime, or the expire_hour one for tokens issued before the
// lifetime was shortened.
func (s *AuthService) revocationTTL() time.Duration {
	ttl := time.Duration(s.GetAccessTokenExpiresIn()) * time.Second
	if legacy := time.Duration(s.cfg.JWT.ExpireHour) * time.Hour; legacy > ttl {
		ttl = legacy
	}
	return ttl + time.Minute
}

func (s *AuthService) revokeSessionAccess(ctx context.Context, sessionID string) {
	cache, ok := s.sessionRevocation()
	if !ok || sessionID == "" {
		return
	}
	if err := cache.RevokeSession(ctx, sessionID, s.revocationTTL()); err != nil {
		logger.LegacyPrintf("service.auth", "[Auth] Failed to revoke access tokens of session %s: %v", sessionID, err)
	}
}

func (s *AuthService) revokeUserAccessBefore(ctx context.Context, userID int64, at time.Time) {
	cache, ok := s.sessionRevocation()
	if !ok {
		return
	}
	if err := cache.SetUserTokensValidAfter(ctx, userID, at, s.revocationTTL()); err != nil {
		logger.LegacyPrintf("service.auth", "[Auth] Failed to revoke access tokens of user %d: %v", userID, err)
	}
}

// RevokeAccessToken ends the session of a still-valid access token (logout
// carrying the bearer token; covers sessions without a refresh token).
func (s *AuthService) RevokeAccessToken(ctx context.Context, tokenString string) {
	claims, err := s.ValidateToken(tokenString)
	if err != nil || claims == nil {
		return
	}
	s.revokeSessionAccess(ctx, claims.SessionID)
}

// CheckAccessTokenRevocation returns ErrTokenRevoked when the token's session
// was revoked or the token predates the user's last revoke-all. Redis failures
// fail open (the token still expires on its own) and are logged.
func (s *AuthService) CheckAccessTokenRevocation(ctx context.Context, claims *JWTClaims) error {
	cache, ok := s.sessionRevocation()
	if !ok || claims == nil {
		return nil
	}
	revoked, validAfter, err := cache.GetSessionRevocation(ctx, claims.SessionID, claims.UserID)
	if err != nil {
		logRevocationCheckFailure(err)
		return nil
	}
	if revoked {
		return ErrTokenRevoked
	}
	if !validAfter.IsZero() && claims.issuedAt().Before(validAfter) {
		return ErrTokenRevoked
	}
	return nil
}

// issuedAt prefers the millisecond issue time; tokens issued before it existed
// fall back to the whole-second iat.
func (c *JWTClaims) issuedAt() time.Time {
	if c.IssuedAtMs > 0 {
		return time.UnixMilli(c.IssuedAtMs)
	}
	if c.IssuedAt != nil {
		return c.IssuedAt.Time
	}
	return time.Time{}
}

func logRevocationCheckFailure(err error) {
	now := time.Now().UnixNano()
	last := lastRevocationCheckFailLog.Load()
	if now-last < int64(revocationCheckFailLogInterval) || !lastRevocationCheckFailLog.CompareAndSwap(last, now) {
		return
	}
	logger.LegacyPrintf("service.auth", "[Auth] Session revocation check failed, allowing request: %v", err)
}
