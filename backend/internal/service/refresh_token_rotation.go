package service

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// RotatedRefreshToken is what remains of a refresh token right after rotation,
// for RefreshReuseGrace: a retry of the same token within the grace window
// (a lost response on a flaky mobile network) receives the same successor
// instead of being treated as a replay.
type RotatedRefreshToken struct {
	UserID      int64     `json:"user_id"`
	FamilyID    string    `json:"family_id"`
	BindingHash string    `json:"binding_hash,omitempty"`
	RotatedAt   time.Time `json:"rotated_at"`
	// SuccessorEnc is the successor refresh token, AES-GCM encrypted with a key
	// derived from the old token's plaintext, so the server never stores a
	// usable refresh token in clear.
	SuccessorEnc string `json:"successor_enc"`
}

// ErrRefreshTokenRotated means the token was already rotated; the caller may
// read the rotated record.
var ErrRefreshTokenRotated = errors.New("refresh token already rotated")

// RefreshTokenRotator atomically swaps a live refresh token for its rotated
// record, and reads rotated records back.
type RefreshTokenRotator interface {
	// RotateRefreshToken replaces tokenHash's live entry with rotated, kept for
	// keep (the old token's remaining lifetime, so a late replay is still
	// recognised). It returns ErrRefreshTokenRotated if another call rotated it
	// first and ErrRefreshTokenNotFound if neither entry exists.
	RotateRefreshToken(ctx context.Context, tokenHash string, rotated *RotatedRefreshToken, keep time.Duration) error
	GetRotatedRefreshToken(ctx context.Context, tokenHash string) (*RotatedRefreshToken, error)
}

const refreshSuccessorKeyInfo = "sub2api refresh successor v1"

func refreshSuccessorAEAD(oldToken string) (cipher.AEAD, error) {
	key, err := hkdf.Key(sha256.New, []byte(oldToken), nil, refreshSuccessorKeyInfo, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func sealRefreshSuccessor(oldToken, successor string) (string, error) {
	aead, err := refreshSuccessorAEAD(oldToken)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, []byte(successor), nil)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func openRefreshSuccessor(oldToken, sealed string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil {
		return "", err
	}
	aead, err := refreshSuccessorAEAD(oldToken)
	if err != nil {
		return "", err
	}
	if len(raw) < aead.NonceSize() {
		return "", fmt.Errorf("successor ciphertext too short")
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func (s *AuthService) refreshRotator() (RefreshTokenRotator, bool) {
	rotator, ok := s.refreshTokenCache.(RefreshTokenRotator)
	return rotator, ok
}

func (s *AuthService) refreshReuseGrace() time.Duration {
	if s.cfg == nil || s.cfg.JWT.RefreshReuseGraceSeconds <= 0 {
		return 0
	}
	return time.Duration(s.cfg.JWT.RefreshReuseGraceSeconds) * time.Second
}

// rotateRefreshToken issues the successor pair first, then atomically swaps
// the old token for a rotated record carrying the sealed successor. If another
// request rotated the same token in between, the freshly issued pair is
// discarded and that request's successor is served instead, so a family never
// forks into two live chains.
func (s *AuthService) rotateRefreshToken(ctx context.Context, rotator RefreshTokenRotator, oldToken, tokenHash string, data *RefreshTokenData, user *User) (*TokenPairWithUser, error) {
	pair, err := s.GenerateTokenPair(ctx, user, data.FamilyID)
	if err != nil {
		return nil, err
	}
	sealed, err := sealRefreshSuccessor(oldToken, pair.RefreshToken)
	if err != nil {
		s.discardRefreshToken(ctx, pair.RefreshToken)
		return nil, fmt.Errorf("seal refresh successor: %w", err)
	}
	keep := time.Until(data.ExpiresAt)
	err = rotator.RotateRefreshToken(ctx, tokenHash, &RotatedRefreshToken{
		UserID: data.UserID, FamilyID: data.FamilyID, BindingHash: data.BindingHash,
		RotatedAt: time.Now(), SuccessorEnc: sealed,
	}, keep)
	switch {
	case err == nil:
		return &TokenPairWithUser{TokenPair: *pair, UserRole: user.Role}, nil
	case errors.Is(err, ErrRefreshTokenRotated):
		// Lost the race: drop our pair and serve the winner's successor.
		s.discardRefreshToken(ctx, pair.RefreshToken)
		return s.refreshFromRotated(ctx, rotator, oldToken, tokenHash)
	case errors.Is(err, ErrRefreshTokenNotFound):
		s.discardRefreshToken(ctx, pair.RefreshToken)
		return nil, ErrRefreshTokenInvalid
	default:
		s.discardRefreshToken(ctx, pair.RefreshToken)
		logger.LegacyPrintf("service.auth", "[Auth] Rotate refresh token failed: %v", err)
		return nil, ErrServiceUnavailable
	}
}

// refreshFromRotated handles a token that was already rotated. Within the
// grace window, from the same session binding, it returns the same successor
// with a fresh access token. Later, it is a replay: the whole family is
// revoked and REFRESH_TOKEN_REUSED returned.
func (s *AuthService) refreshFromRotated(ctx context.Context, rotator RefreshTokenRotator, oldToken, tokenHash string) (*TokenPairWithUser, error) {
	rotated, err := rotator.GetRotatedRefreshToken(ctx, tokenHash)
	if err != nil {
		if errors.Is(err, ErrRefreshTokenNotFound) {
			return nil, ErrRefreshTokenInvalid
		}
		logger.LegacyPrintf("service.auth", "[Auth] Error getting rotated refresh token: %v", err)
		return nil, ErrServiceUnavailable
	}
	grace := s.refreshReuseGrace()
	if grace <= 0 || time.Since(rotated.RotatedAt) > grace {
		s.revokeReplayedFamily(ctx, rotated)
		return nil, ErrRefreshTokenReused
	}
	if s.settingService != nil && s.settingService.IsSessionBindingEnabled(ctx) && rotated.BindingHash != "" {
		if !sessionBindingMatches(ctx, rotated.BindingHash) {
			s.revokeReplayedFamily(ctx, rotated)
			return nil, ErrSessionBindingMismatch
		}
	}
	successor, err := openRefreshSuccessor(oldToken, rotated.SuccessorEnc)
	if err != nil {
		return nil, ErrRefreshTokenInvalid
	}
	// The successor must still be live. A client that lost the response cannot
	// have used it yet, so an old token coming back after its successor was
	// already rotated (or revoked) is treated as a replay.
	successorData, err := s.refreshTokenCache.GetRefreshToken(ctx, hashToken(successor))
	if err != nil {
		if errors.Is(err, ErrRefreshTokenNotFound) {
			s.revokeReplayedFamily(ctx, rotated)
			return nil, ErrRefreshTokenReused
		}
		return nil, ErrServiceUnavailable
	}
	user, err := s.userRepo.GetByID(ctx, successorData.UserID)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil, ErrRefreshTokenInvalid
		}
		return nil, ErrServiceUnavailable
	}
	if !user.IsActive() {
		return nil, ErrUserNotActive
	}
	if successorData.TokenVersion != resolvedTokenVersion(user) {
		return nil, ErrTokenRevoked
	}
	accessToken, err := s.generateAccessToken(user, successorData.FamilyID, successorData.BindingHash)
	if err != nil {
		return nil, fmt.Errorf("generate access token: %w", err)
	}
	return &TokenPairWithUser{
		TokenPair: TokenPair{AccessToken: accessToken, RefreshToken: successor, ExpiresIn: s.GetAccessTokenExpiresIn()},
		UserRole:  user.Role,
	}, nil
}

func (s *AuthService) revokeReplayedFamily(ctx context.Context, rotated *RotatedRefreshToken) {
	if err := s.refreshTokenCache.DeleteTokenFamily(ctx, rotated.FamilyID); err != nil {
		logger.LegacyPrintf("service.auth", "[Auth] Failed to revoke replayed refresh family: %v", err)
	}
	logger.LegacyPrintf("service.auth", "[Auth] Refresh token replayed after rotation for user %d, family revoked", rotated.UserID)
}

func (s *AuthService) discardRefreshToken(ctx context.Context, token string) {
	if err := s.refreshTokenCache.DeleteRefreshToken(ctx, hashToken(token)); err != nil {
		logger.LegacyPrintf("service.auth", "[Auth] Failed to discard unused refresh token: %v", err)
	}
}
