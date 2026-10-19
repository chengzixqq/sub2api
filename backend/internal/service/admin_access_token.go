package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// Admin access tokens (sat_...) are per-device admin credentials: named,
// read or write scoped, expiring, individually revocable, and acting as the
// admin who created them (unlike the single site-wide Admin API Key, which
// always acts as the first admin).
const (
	AdminAccessTokenPrefix = "sat_"

	AdminAccessTokenScopeRead  = "read"
	AdminAccessTokenScopeWrite = "write"

	AuditAuthMethodAdminAccessToken = "admin_access_token"

	defaultAdminAccessTokenDays = 90
	maxAdminAccessTokenDays     = 365
	maxAdminAccessTokensPerUser = 20
	adminAccessTokenCacheTTL    = 30 * time.Second
	adminAccessTokenCacheMax    = 1024
	adminAccessTokenTouchEvery  = 5 * time.Minute
)

var (
	ErrAdminAccessTokenInvalid     = infraerrors.Unauthorized("INVALID_ADMIN_KEY", "Invalid admin API key")
	ErrAdminAccessTokenExpired     = infraerrors.Unauthorized("ACCESS_TOKEN_EXPIRED", "admin access token has expired")
	ErrAdminAccessTokenReadOnly    = infraerrors.Forbidden("ACCESS_TOKEN_READ_ONLY", "this admin access token is read-only")
	ErrAdminAccessTokenNotFound    = infraerrors.NotFound("ACCESS_TOKEN_NOT_FOUND", "admin access token not found")
	ErrAdminAccessTokenLimit       = infraerrors.Conflict("ACCESS_TOKEN_LIMIT_REACHED", "too many active admin access tokens; revoke one first")
	ErrAdminAccessTokenName        = infraerrors.BadRequest("ACCESS_TOKEN_NAME_REQUIRED", "name is required (at most 64 characters)")
	ErrAdminAccessTokenScope       = infraerrors.BadRequest("ACCESS_TOKEN_INVALID_SCOPE", "scope must be read or write")
	ErrAdminAccessTokenExpiry      = infraerrors.BadRequest("ACCESS_TOKEN_INVALID_EXPIRY", "expires_in_days must be between 1 and 365")
	ErrAdminAccessTokenNeedSession = infraerrors.Forbidden("STEP_UP_ADMIN_API_KEY_FORBIDDEN", "admin access tokens can only be created from a signed-in admin session")
)

// AdminAccessToken is a stored token; the plaintext is never stored.
type AdminAccessToken struct {
	ID         int64
	UserID     int64
	Name       string
	TokenHash  string
	Prefix     string
	Last4      string
	Scope      string
	ExpiresAt  time.Time
	LastUsedAt *time.Time
	LastUsedIP string
	RevokedAt  *time.Time
	CreatedAt  time.Time
}

// ReadOnly reports whether the token may only read.
func (t *AdminAccessToken) ReadOnly() bool {
	return t.Scope != AdminAccessTokenScopeWrite
}

type AdminAccessTokenRepository interface {
	Create(ctx context.Context, token *AdminAccessToken) error
	GetByHash(ctx context.Context, tokenHash string) (*AdminAccessToken, error)
	// ListByUser returns the user's tokens that are neither revoked nor expired, newest first.
	ListByUser(ctx context.Context, userID int64, now time.Time) ([]*AdminAccessToken, error)
	CountActiveByUser(ctx context.Context, userID int64, now time.Time) (int, error)
	// Revoke marks the user's token revoked and returns it; ErrAdminAccessTokenNotFound
	// when it does not exist, belongs to someone else or is already revoked.
	Revoke(ctx context.Context, userID, id int64, now time.Time) (*AdminAccessToken, error)
	TouchLastUsed(ctx context.Context, id int64, at time.Time, ip string) error
}

// CreateAdminAccessTokenInput is a creation request.
type CreateAdminAccessTokenInput struct {
	UserID        int64
	Name          string
	Scope         string
	ExpiresInDays int
}

// CreatedAdminAccessToken carries the plaintext token, shown only once.
type CreatedAdminAccessToken struct {
	Token  string
	Record *AdminAccessToken
}

type cachedAdminAccessToken struct {
	token    *AdminAccessToken
	cachedAt time.Time
}

type AdminAccessTokenService struct {
	repo AdminAccessTokenRepository
	now  func() time.Time

	mu    sync.Mutex
	cache map[string]*cachedAdminAccessToken
}

func NewAdminAccessTokenService(repo AdminAccessTokenRepository) *AdminAccessTokenService {
	return &AdminAccessTokenService{repo: repo, now: time.Now, cache: map[string]*cachedAdminAccessToken{}}
}

// IsAdminAccessToken reports whether a credential is an sat_ token.
func IsAdminAccessToken(credential string) bool {
	return strings.HasPrefix(credential, AdminAccessTokenPrefix)
}

// IsMachineCredential reports whether an auth method is a credential that
// cannot complete two-factor step-up (Admin API Key, admin access token).
func IsMachineCredential(authMethod string) bool {
	return authMethod == AuditAuthMethodAdminAPIKey || authMethod == AuditAuthMethodAdminAccessToken
}

func (s *AdminAccessTokenService) Create(ctx context.Context, in CreateAdminAccessTokenInput) (*CreatedAdminAccessToken, error) {
	name := NormalizeSessionDeviceName(in.Name)
	if name == "" {
		return nil, ErrAdminAccessTokenName
	}
	scope := strings.TrimSpace(in.Scope)
	if scope == "" {
		scope = AdminAccessTokenScopeRead
	}
	if scope != AdminAccessTokenScopeRead && scope != AdminAccessTokenScopeWrite {
		return nil, ErrAdminAccessTokenScope
	}
	days := in.ExpiresInDays
	if days == 0 {
		days = defaultAdminAccessTokenDays
	}
	if days < 1 || days > maxAdminAccessTokenDays {
		return nil, ErrAdminAccessTokenExpiry
	}
	now := s.now()
	active, err := s.repo.CountActiveByUser(ctx, in.UserID, now)
	if err != nil {
		return nil, err
	}
	if active >= maxAdminAccessTokensPerUser {
		return nil, ErrAdminAccessTokenLimit
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	plain := AdminAccessTokenPrefix + hex.EncodeToString(raw)
	record := &AdminAccessToken{
		UserID:    in.UserID,
		Name:      name,
		TokenHash: hashToken(plain),
		Prefix:    plain[:len(AdminAccessTokenPrefix)+4],
		Last4:     plain[len(plain)-4:],
		Scope:     scope,
		ExpiresAt: now.Add(time.Duration(days) * 24 * time.Hour),
	}
	if err := s.repo.Create(ctx, record); err != nil {
		return nil, err
	}
	return &CreatedAdminAccessToken{Token: plain, Record: record}, nil
}

func (s *AdminAccessTokenService) List(ctx context.Context, userID int64) ([]*AdminAccessToken, error) {
	return s.repo.ListByUser(ctx, userID, s.now())
}

// Revoke revokes one of the user's tokens. Other instances notice within the
// 30-second cache window.
func (s *AdminAccessTokenService) Revoke(ctx context.Context, userID, id int64) error {
	token, err := s.repo.Revoke(ctx, userID, id, s.now())
	if err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.cache, token.TokenHash)
	s.mu.Unlock()
	return nil
}

// Authenticate resolves an sat_ token. Unknown and revoked tokens are
// ErrAdminAccessTokenInvalid, the same answer as a wrong Admin API Key.
func (s *AdminAccessTokenService) Authenticate(ctx context.Context, plain, clientIP string) (*AdminAccessToken, error) {
	if !IsAdminAccessToken(plain) {
		return nil, ErrAdminAccessTokenInvalid
	}
	hash := hashToken(plain)
	now := s.now()
	token, err := s.lookup(ctx, hash, now)
	if err != nil {
		return nil, err
	}
	if token.RevokedAt != nil {
		return nil, ErrAdminAccessTokenInvalid
	}
	if !now.Before(token.ExpiresAt) {
		return nil, ErrAdminAccessTokenExpired
	}
	s.touch(ctx, token, now, clientIP)
	return token, nil
}

func (s *AdminAccessTokenService) lookup(ctx context.Context, hash string, now time.Time) (*AdminAccessToken, error) {
	s.mu.Lock()
	if hit, ok := s.cache[hash]; ok && now.Sub(hit.cachedAt) < adminAccessTokenCacheTTL {
		s.mu.Unlock()
		return hit.token, nil
	}
	s.mu.Unlock()

	token, err := s.repo.GetByHash(ctx, hash)
	if err != nil {
		if errors.Is(err, ErrAdminAccessTokenNotFound) {
			return nil, ErrAdminAccessTokenInvalid
		}
		return nil, err
	}
	s.mu.Lock()
	if len(s.cache) >= adminAccessTokenCacheMax {
		s.cache = map[string]*cachedAdminAccessToken{}
	}
	s.cache[hash] = &cachedAdminAccessToken{token: token, cachedAt: now}
	s.mu.Unlock()
	return token, nil
}

// touch records last use at most every five minutes per token.
func (s *AdminAccessTokenService) touch(ctx context.Context, token *AdminAccessToken, now time.Time, ip string) {
	s.mu.Lock()
	if token.LastUsedAt != nil && now.Sub(*token.LastUsedAt) < adminAccessTokenTouchEvery {
		s.mu.Unlock()
		return
	}
	updated := *token
	updated.LastUsedAt = &now
	updated.LastUsedIP = ip
	if hit, ok := s.cache[token.TokenHash]; ok {
		hit.token = &updated
	}
	s.mu.Unlock()
	if err := s.repo.TouchLastUsed(ctx, token.ID, now, ip); err != nil {
		logger.LegacyPrintf("service.admin_access_token", "[AdminAccessToken] Failed to record last use of token %d: %v", token.ID, err)
	}
}
