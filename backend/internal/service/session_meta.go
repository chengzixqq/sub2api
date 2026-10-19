package service

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// SessionMeta describes one login session (a refresh token family; its id is
// the access token sid) for the session list.
type SessionMeta struct {
	ID         string
	UserID     int64
	DeviceName string
	ClientType string
	UserAgent  string
	IP         string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

// SessionMetaCache stores session metadata next to the refresh tokens. The
// Redis refresh token cache implements it; deleting a token family or all of a
// user's refresh tokens deletes the metadata too.
type SessionMetaCache interface {
	// SaveSessionMeta creates or refreshes a session. Fields only the login
	// knows (created_at, device_name, client_type) keep their first value.
	SaveSessionMeta(ctx context.Context, meta *SessionMeta, ttl time.Duration) error
	// GetSessionMeta returns ErrSessionNotFound when the session is gone.
	GetSessionMeta(ctx context.Context, sessionID string) (*SessionMeta, error)
	// ListUserSessions returns live sessions, most recently seen first.
	ListUserSessions(ctx context.Context, userID int64, limit int) ([]*SessionMeta, error)
}

var ErrSessionNotFound = infraerrors.NotFound("SESSION_NOT_FOUND", "session not found")

const (
	maxListedSessions       = 50
	maxSessionDeviceNameLen = 64
	sessionClientTypeWeb    = "web"
)

// NormalizeSessionDeviceName trims a client-declared device name, drops
// control characters and keeps at most 64 characters.
func NormalizeSessionDeviceName(name string) string {
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if runes := []rune(name); len(runes) > maxSessionDeviceNameLen {
		name = strings.TrimSpace(string(runes[:maxSessionDeviceNameLen]))
	}
	return name
}

func (s *AuthService) sessionMeta() (SessionMetaCache, bool) {
	if s.refreshTokenCache == nil {
		return nil, false
	}
	cache, ok := s.refreshTokenCache.(SessionMetaCache)
	return cache, ok
}

// recordSession saves a session's metadata when a token pair is issued: a new
// login records the declared client; a rotation only moves last_seen forward.
func (s *AuthService) recordSession(ctx context.Context, userID int64, sessionID string, isNew bool) {
	cache, ok := s.sessionMeta()
	if !ok {
		return
	}
	now := time.Now()
	ttl := time.Duration(s.cfg.JWT.RefreshTokenExpireDays) * 24 * time.Hour
	meta := &SessionMeta{ID: sessionID, UserID: userID, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(ttl)}
	if binding := SessionBindingFromContext(ctx); binding != nil {
		meta.IP = binding.IP
		meta.UserAgent = binding.UserAgent
	}
	if isNew {
		meta.ClientType = sessionClientTypeWeb
		if client := sessionClientFromContext(ctx); client.IsNative() {
			meta.ClientType = strings.ToLower(strings.TrimSpace(client.Type))
			meta.DeviceName = NormalizeSessionDeviceName(client.DeviceName)
		}
	}
	if err := cache.SaveSessionMeta(ctx, meta, ttl); err != nil {
		logger.LegacyPrintf("service.auth", "[Auth] Failed to record session %s: %v", sessionID, err)
	}
}

// ListUserSessions lists the user's live login sessions (at most 50).
func (s *AuthService) ListUserSessions(ctx context.Context, userID int64) ([]*SessionMeta, error) {
	cache, ok := s.sessionMeta()
	if !ok {
		return []*SessionMeta{}, nil
	}
	sessions, err := cache.ListUserSessions(ctx, userID, maxListedSessions)
	if err != nil {
		logger.LegacyPrintf("service.auth", "[Auth] Failed to list sessions of user %d: %v", userID, err)
		return nil, ErrServiceUnavailable
	}
	return sessions, nil
}

// RevokeUserSession ends one of the user's sessions: its refresh tokens are
// deleted and its access tokens stop working. Unknown ids and other users'
// sessions are both ErrSessionNotFound.
func (s *AuthService) RevokeUserSession(ctx context.Context, userID int64, sessionID string) error {
	cache, ok := s.sessionMeta()
	if !ok || strings.TrimSpace(sessionID) == "" {
		return ErrSessionNotFound
	}
	meta, err := cache.GetSessionMeta(ctx, sessionID)
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			return ErrSessionNotFound
		}
		return ErrServiceUnavailable
	}
	if meta.UserID != userID {
		return ErrSessionNotFound
	}
	return s.RevokeSessionFamily(ctx, sessionID)
}

// RevokeOtherUserSessions signs the user out everywhere except keepSessionID:
// earlier access tokens of other sessions stop working and their refresh
// tokens are deleted.
func (s *AuthService) RevokeOtherUserSessions(ctx context.Context, userID int64, keepSessionID string) error {
	if keepSessionID == "" {
		return s.RevokeAllUserTokens(ctx, userID)
	}
	if _, err := s.userRepo.GetByID(ctx, userID); err != nil {
		return err
	}
	s.revokeUserAccessBeforeExcept(ctx, userID, time.Now(), keepSessionID)
	if s.refreshTokenCache == nil {
		return nil
	}
	hashes, err := s.refreshTokenCache.GetUserTokenHashes(ctx, userID)
	if err != nil {
		return ErrServiceUnavailable
	}
	revoked := map[string]bool{keepSessionID: true}
	for _, hash := range hashes {
		data, err := s.refreshTokenCache.GetRefreshToken(ctx, hash)
		if err != nil || data == nil || revoked[data.FamilyID] {
			continue
		}
		revoked[data.FamilyID] = true
		if err := s.RevokeSessionFamily(ctx, data.FamilyID); err != nil {
			logger.LegacyPrintf("service.auth", "[Auth] Failed to revoke session %s of user %d: %v", data.FamilyID, userID, err)
		}
	}
	return nil
}
