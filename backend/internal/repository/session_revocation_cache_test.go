package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSessionRevocation_LogoutRevokesAccessTokenImmediately(t *testing.T) {
	svc, _, user := newRotationAuthService(t, 30)
	ctx := context.Background()
	pair, err := svc.GenerateTokenPair(ctx, user, "")
	require.NoError(t, err)
	claims, err := svc.ValidateToken(pair.AccessToken)
	require.NoError(t, err)
	require.NoError(t, svc.CheckAccessTokenRevocation(ctx, claims))

	require.NoError(t, svc.RevokeRefreshToken(ctx, pair.RefreshToken))
	require.ErrorIs(t, svc.CheckAccessTokenRevocation(ctx, claims), service.ErrTokenRevoked)

	// A new login is a new session and is not affected.
	again, err := svc.GenerateTokenPair(ctx, user, "")
	require.NoError(t, err)
	againClaims, err := svc.ValidateToken(again.AccessToken)
	require.NoError(t, err)
	require.NoError(t, svc.CheckAccessTokenRevocation(ctx, againClaims))
}

func TestSessionRevocation_LogoutWithBearerRevokesSessionWithoutRefreshToken(t *testing.T) {
	svc, _, user := newRotationAuthService(t, 30)
	ctx := context.Background()
	token, err := svc.GenerateToken(ctx, user)
	require.NoError(t, err)
	claims, err := svc.ValidateToken(token)
	require.NoError(t, err)

	svc.RevokeAccessToken(ctx, token)
	require.ErrorIs(t, svc.CheckAccessTokenRevocation(ctx, claims), service.ErrTokenRevoked)
}

func TestSessionRevocation_LogoutWithRotatedTokenRevokesSuccessor(t *testing.T) {
	svc, _, user := newRotationAuthService(t, 30)
	ctx := context.Background()
	pair, err := svc.GenerateTokenPair(ctx, user, "")
	require.NoError(t, err)
	next, err := svc.RefreshTokenPair(ctx, pair.RefreshToken)
	require.NoError(t, err)
	nextClaims, err := svc.ValidateToken(next.AccessToken)
	require.NoError(t, err)

	// Logout still carrying the pre-rotation token ends the whole session.
	require.NoError(t, svc.RevokeRefreshToken(ctx, pair.RefreshToken))
	require.ErrorIs(t, svc.CheckAccessTokenRevocation(ctx, nextClaims), service.ErrTokenRevoked)
	_, err = svc.RefreshTokenPair(ctx, next.RefreshToken)
	require.Error(t, err)
}

func TestSessionRevocation_RevokeAllKillsEarlierTokensOnly(t *testing.T) {
	svc, _, user := newRotationAuthService(t, 30)
	ctx := context.Background()
	deviceA, err := svc.GenerateTokenPair(ctx, user, "")
	require.NoError(t, err)
	legacy, err := svc.GenerateToken(ctx, user)
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond)

	require.NoError(t, svc.RevokeAllUserTokens(ctx, user.ID))

	for _, token := range []string{deviceA.AccessToken, legacy} {
		claims, err := svc.ValidateToken(token)
		require.NoError(t, err)
		require.ErrorIs(t, svc.CheckAccessTokenRevocation(ctx, claims), service.ErrTokenRevoked)
	}

	fresh, err := svc.GenerateTokenPair(ctx, user, "")
	require.NoError(t, err)
	freshClaims, err := svc.ValidateToken(fresh.AccessToken)
	require.NoError(t, err)
	require.NoError(t, svc.CheckAccessTokenRevocation(ctx, freshClaims), "logging in again right after works")
}

func TestSessionRevocation_ReplayRevokesFamilyAccessTokens(t *testing.T) {
	svc, _, user := newRotationAuthService(t, 0)
	ctx := context.Background()
	pair, err := svc.GenerateTokenPair(ctx, user, "")
	require.NoError(t, err)
	next, err := svc.RefreshTokenPair(ctx, pair.RefreshToken)
	require.NoError(t, err)
	nextClaims, err := svc.ValidateToken(next.AccessToken)
	require.NoError(t, err)

	_, err = svc.RefreshTokenPair(ctx, pair.RefreshToken)
	require.Error(t, err)
	require.ErrorIs(t, svc.CheckAccessTokenRevocation(ctx, nextClaims), service.ErrTokenRevoked)
}

func TestSessionRevocation_LegacyAccessTokenRefreshRejectsRevoked(t *testing.T) {
	svc, _, user := newRotationAuthService(t, 30)
	ctx := context.Background()
	token, err := svc.GenerateToken(ctx, user)
	require.NoError(t, err)
	svc.RevokeAccessToken(ctx, token)

	_, err = svc.RefreshToken(ctx, token)
	require.ErrorIs(t, err, service.ErrTokenRevoked)
}

func TestSessionRevocation_RedisDownFailsOpen(t *testing.T) {
	svc, server, user := newRotationAuthService(t, 30)
	ctx := context.Background()
	pair, err := svc.GenerateTokenPair(ctx, user, "")
	require.NoError(t, err)
	claims, err := svc.ValidateToken(pair.AccessToken)
	require.NoError(t, err)

	server.Close()
	require.NoError(t, svc.CheckAccessTokenRevocation(ctx, claims))
}

func TestSessionRevocation_KeysOutliveTheAccessToken(t *testing.T) {
	svc, server, user := newRotationAuthService(t, 30)
	ctx := context.Background()
	pair, err := svc.GenerateTokenPair(ctx, user, "")
	require.NoError(t, err)
	claims, err := svc.ValidateToken(pair.AccessToken)
	require.NoError(t, err)
	require.NoError(t, svc.RevokeRefreshToken(ctx, pair.RefreshToken))

	ttl := server.TTL("revoked_sid:" + claims.SessionID)
	require.GreaterOrEqual(t, ttl, time.Duration(svc.GetAccessTokenExpiresIn())*time.Second)
}
