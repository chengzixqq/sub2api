package repository

import (
	"context"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func sessionCtx(ip, ua string, client *service.SessionClient) context.Context {
	ctx := service.WithSessionBinding(context.Background(), &service.SessionBinding{IP: ip, UserAgent: ua})
	return service.WithSessionClient(ctx, client)
}

func sidOf(t *testing.T, svc *service.AuthService, accessToken string) string {
	t.Helper()
	claims, err := svc.ValidateToken(accessToken)
	require.NoError(t, err)
	return claims.SessionID
}

func TestSessions_NativeAndWebLoginsAreListed(t *testing.T) {
	svc, _, user := newRotationAuthService(t, 30)
	phone, err := svc.GenerateTokenPair(sessionCtx("203.0.113.24", "Sub2Mobile-iOS", &service.SessionClient{Type: "ios", DeviceID: "D1", DeviceName: " 我的 iPhone\x07 "}), user, "")
	require.NoError(t, err)
	time.Sleep(3 * time.Millisecond)
	web, err := svc.GenerateTokenPair(sessionCtx("198.51.100.7", "Mozilla/5.0", nil), user, "")
	require.NoError(t, err)

	sessions, err := svc.ListUserSessions(context.Background(), user.ID)
	require.NoError(t, err)
	require.Len(t, sessions, 2)

	require.Equal(t, sidOf(t, svc, web.AccessToken), sessions[0].ID, "most recently seen first")
	require.Equal(t, "web", sessions[0].ClientType)
	require.Equal(t, "", sessions[0].DeviceName)
	require.Equal(t, "Mozilla/5.0", sessions[0].UserAgent)

	ios := sessions[1]
	require.Equal(t, sidOf(t, svc, phone.AccessToken), ios.ID)
	require.Equal(t, "我的 iPhone", ios.DeviceName, "trimmed, control characters removed")
	require.Equal(t, "ios", ios.ClientType)
	require.Equal(t, "203.0.113.24", ios.IP)
	require.False(t, ios.CreatedAt.IsZero())
	require.False(t, ios.LastSeenAt.Before(ios.CreatedAt))
	require.True(t, ios.ExpiresAt.After(time.Now().Add(29*24*time.Hour)))
}

func TestSessions_RefreshUpdatesLastSeenAndKeepsLoginFields(t *testing.T) {
	svc, _, user := newRotationAuthService(t, 30)
	phone, err := svc.GenerateTokenPair(sessionCtx("203.0.113.24", "Sub2Mobile-iOS", &service.SessionClient{Type: "ios", DeviceName: "我的 iPhone"}), user, "")
	require.NoError(t, err)
	time.Sleep(3 * time.Millisecond)
	_, err = svc.GenerateTokenPair(sessionCtx("198.51.100.7", "Mozilla/5.0", nil), user, "")
	require.NoError(t, err)
	before, err := svc.ListUserSessions(context.Background(), user.ID)
	require.NoError(t, err)
	created := before[1].CreatedAt

	time.Sleep(3 * time.Millisecond)
	_, err = svc.RefreshTokenPair(sessionCtx("192.0.2.50", "Sub2Mobile-iOS", nil), phone.RefreshToken)
	require.NoError(t, err)

	after, err := svc.ListUserSessions(context.Background(), user.ID)
	require.NoError(t, err)
	require.Len(t, after, 2)
	require.Equal(t, sidOf(t, svc, phone.AccessToken), after[0].ID, "refreshed session moves to the top")
	require.Equal(t, "我的 iPhone", after[0].DeviceName)
	require.Equal(t, "ios", after[0].ClientType)
	require.Equal(t, "192.0.2.50", after[0].IP, "latest IP")
	require.True(t, after[0].CreatedAt.Equal(created))
	require.True(t, after[0].LastSeenAt.After(created))
}

func TestSessions_RevokeOneSession(t *testing.T) {
	svc, _, user := newRotationAuthService(t, 30)
	ctx := context.Background()
	current, err := svc.GenerateTokenPair(sessionCtx("203.0.113.24", "Sub2Mobile-iOS", nil), user, "")
	require.NoError(t, err)
	lost, err := svc.GenerateTokenPair(sessionCtx("198.51.100.7", "Sub2Mobile-iOS", nil), user, "")
	require.NoError(t, err)
	lostClaims, err := svc.ValidateToken(lost.AccessToken)
	require.NoError(t, err)

	require.NoError(t, svc.RevokeUserSession(ctx, user.ID, lostClaims.SessionID))

	_, err = svc.RefreshTokenPair(ctx, lost.RefreshToken)
	require.Equal(t, "REFRESH_TOKEN_INVALID", infraerrors.Reason(err))
	require.ErrorIs(t, svc.CheckAccessTokenRevocation(ctx, lostClaims), service.ErrTokenRevoked)
	sessions, err := svc.ListUserSessions(ctx, user.ID)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.Equal(t, sidOf(t, svc, current.AccessToken), sessions[0].ID)

	require.ErrorIs(t, svc.RevokeUserSession(ctx, user.ID, lostClaims.SessionID), service.ErrSessionNotFound)
	require.ErrorIs(t, svc.RevokeUserSession(ctx, user.ID, "no-such-session"), service.ErrSessionNotFound)
	require.ErrorIs(t, svc.RevokeUserSession(ctx, user.ID+1, sessions[0].ID), service.ErrSessionNotFound, "another user's session")
}

func TestSessions_RevokeAllKeepingCurrent(t *testing.T) {
	svc, _, user := newRotationAuthService(t, 30)
	ctx := context.Background()
	current, err := svc.GenerateTokenPair(sessionCtx("203.0.113.24", "Sub2Mobile-iOS", nil), user, "")
	require.NoError(t, err)
	other, err := svc.GenerateTokenPair(sessionCtx("198.51.100.7", "Mozilla/5.0", nil), user, "")
	require.NoError(t, err)
	legacy, err := svc.GenerateToken(ctx, user)
	require.NoError(t, err)
	time.Sleep(3 * time.Millisecond)
	currentSID := sidOf(t, svc, current.AccessToken)

	require.NoError(t, svc.RevokeOtherUserSessions(ctx, user.ID, currentSID))

	for _, token := range []string{other.AccessToken, legacy} {
		claims, err := svc.ValidateToken(token)
		require.NoError(t, err)
		require.ErrorIs(t, svc.CheckAccessTokenRevocation(ctx, claims), service.ErrTokenRevoked)
	}
	_, err = svc.RefreshTokenPair(ctx, other.RefreshToken)
	require.Error(t, err)

	currentClaims, err := svc.ValidateToken(current.AccessToken)
	require.NoError(t, err)
	require.NoError(t, svc.CheckAccessTokenRevocation(ctx, currentClaims), "the current session survives")
	_, err = svc.RefreshTokenPair(sessionCtx("203.0.113.24", "Sub2Mobile-iOS", nil), current.RefreshToken)
	require.NoError(t, err)

	sessions, err := svc.ListUserSessions(ctx, user.ID)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.Equal(t, currentSID, sessions[0].ID)
}

func TestSessions_LogoutAndRevokeAllClearTheList(t *testing.T) {
	svc, _, user := newRotationAuthService(t, 30)
	ctx := context.Background()
	a, err := svc.GenerateTokenPair(sessionCtx("203.0.113.24", "ua", nil), user, "")
	require.NoError(t, err)
	_, err = svc.GenerateTokenPair(sessionCtx("198.51.100.7", "ua", nil), user, "")
	require.NoError(t, err)

	require.NoError(t, svc.RevokeRefreshToken(ctx, a.RefreshToken))
	sessions, err := svc.ListUserSessions(ctx, user.ID)
	require.NoError(t, err)
	require.Len(t, sessions, 1)

	require.NoError(t, svc.RevokeAllUserTokens(ctx, user.ID))
	sessions, err = svc.ListUserSessions(ctx, user.ID)
	require.NoError(t, err)
	require.Empty(t, sessions)
}
