package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type rotationUserRepo struct {
	service.UserRepository
	user *service.User
}

func (r rotationUserRepo) GetByID(context.Context, int64) (*service.User, error) {
	copied := *r.user
	return &copied, nil
}

func newRotationAuthService(t *testing.T, grace int) (*service.AuthService, *miniredis.Miniredis, *service.User) {
	t.Helper()
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	user := &service.User{ID: 7, Email: "u@example.com", Role: service.RoleAdmin, Status: service.StatusActive}
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "rotation-test-secret", ExpireHour: 1, RefreshTokenExpireDays: 30, RefreshReuseGraceSeconds: grace}}
	svc := service.NewAuthService(nil, rotationUserRepo{user: user}, nil, NewRefreshTokenCache(rdb), cfg, nil, nil, nil, nil, nil, nil, nil, nil)
	return svc, server, user
}

func TestRefreshRotation_RetryWithinGraceGetsSameSuccessor(t *testing.T) {
	svc, _, user := newRotationAuthService(t, 1)
	ctx := context.Background()
	pair, err := svc.GenerateTokenPair(ctx, user, "")
	require.NoError(t, err)

	first, err := svc.RefreshTokenPair(ctx, pair.RefreshToken)
	require.NoError(t, err)
	require.NotEqual(t, pair.RefreshToken, first.RefreshToken)

	// The response was lost; the client retries with the old token.
	again, err := svc.RefreshTokenPair(ctx, pair.RefreshToken)
	require.NoError(t, err)
	require.Equal(t, first.RefreshToken, again.RefreshToken, "same successor within grace")
	require.NotEmpty(t, again.AccessToken)

	// Past the grace window the old token is a replay: the family is revoked.
	time.Sleep(1100 * time.Millisecond)
	_, err = svc.RefreshTokenPair(ctx, pair.RefreshToken)
	require.Equal(t, "REFRESH_TOKEN_REUSED", infraerrors.Reason(err))
	_, err = svc.RefreshTokenPair(ctx, first.RefreshToken)
	require.Error(t, err, "the whole family is revoked after a replay")
}

func TestRefreshRotation_OldTokenAfterSuccessorUsedIsReplay(t *testing.T) {
	svc, _, user := newRotationAuthService(t, 30)
	ctx := context.Background()
	pair, err := svc.GenerateTokenPair(ctx, user, "")
	require.NoError(t, err)
	first, err := svc.RefreshTokenPair(ctx, pair.RefreshToken)
	require.NoError(t, err)
	next, err := svc.RefreshTokenPair(ctx, first.RefreshToken)
	require.NoError(t, err)

	// Within grace, but the successor was already used: someone else holds the old token.
	_, err = svc.RefreshTokenPair(ctx, pair.RefreshToken)
	require.Equal(t, "REFRESH_TOKEN_REUSED", infraerrors.Reason(err))
	_, err = svc.RefreshTokenPair(ctx, next.RefreshToken)
	require.Error(t, err, "family revoked")
}

func TestRefreshRotation_ConcurrentRefreshesShareOneSuccessor(t *testing.T) {
	svc, server, user := newRotationAuthService(t, 30)
	ctx := context.Background()
	pair, err := svc.GenerateTokenPair(ctx, user, "")
	require.NoError(t, err)
	familyBefore := len(server.Keys())

	const n = 10
	results := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := svc.RefreshTokenPair(ctx, pair.RefreshToken)
			errs[i] = err
			if err == nil {
				results[i] = res.RefreshToken
			}
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		require.NoError(t, errs[i])
		require.Equal(t, results[0], results[i], "all concurrent refreshes return the same successor")
	}
	live := 0
	for _, key := range server.Keys() {
		if len(key) > len("refresh_token:") && key[:len("refresh_token:")] == "refresh_token:" {
			live++
		}
	}
	require.Equal(t, 1, live, "exactly one live refresh token remains in the family (keys before: %d)", familyBefore)
}

func TestRefreshRotation_ZeroGraceTreatsAnyReuseAsReplay(t *testing.T) {
	svc, _, user := newRotationAuthService(t, 0)
	ctx := context.Background()
	pair, err := svc.GenerateTokenPair(ctx, user, "")
	require.NoError(t, err)
	first, err := svc.RefreshTokenPair(ctx, pair.RefreshToken)
	require.NoError(t, err)

	_, err = svc.RefreshTokenPair(ctx, pair.RefreshToken)
	require.Equal(t, "REFRESH_TOKEN_REUSED", infraerrors.Reason(err))
	_, err = svc.RefreshTokenPair(ctx, first.RefreshToken)
	require.Error(t, err, "family revoked")
}

func TestRefreshRotation_UnknownTokenIsInvalid(t *testing.T) {
	svc, _, _ := newRotationAuthService(t, 30)
	_, err := svc.RefreshTokenPair(context.Background(), "rt_0000000000000000000000000000000000000000000000000000000000000000")
	require.Equal(t, "REFRESH_TOKEN_INVALID", infraerrors.Reason(err))
}

type bindingSettingRepo struct {
	service.SettingRepository
	values map[string]string
}

func (r bindingSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	if v, ok := r.values[key]; ok {
		return v, nil
	}
	return "", service.ErrSettingNotFound
}

func newBindingAuthService(t *testing.T, nativeMode string) (*service.AuthService, *service.User) {
	t.Helper()
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	user := &service.User{ID: 9, Email: "b@example.com", Role: service.RoleAdmin, Status: service.StatusActive}
	cfg := &config.Config{JWT: config.JWTConfig{Secret: "binding-test-secret", ExpireHour: 1, RefreshTokenExpireDays: 30, RefreshReuseGraceSeconds: 30}}
	settings := service.NewSettingService(bindingSettingRepo{values: map[string]string{
		service.SettingKeySessionBindingEnabled:    "true",
		service.SettingKeySessionBindingNativeMode: nativeMode,
	}}, cfg)
	svc := service.NewAuthService(nil, rotationUserRepo{user: user}, nil, NewRefreshTokenCache(rdb), cfg, settings, nil, nil, nil, nil, nil, nil, nil)
	return svc, user
}

func bindingCtx(ip, deviceID string, client *service.SessionClient) context.Context {
	ctx := service.WithSessionBinding(context.Background(), &service.SessionBinding{IP: ip, UserAgent: "Sub2Mobile-iOS", DeviceID: deviceID})
	return service.WithSessionClient(ctx, client)
}

func TestSessionBinding_NativeDeviceSessionSurvivesNetworkChange(t *testing.T) {
	svc, user := newBindingAuthService(t, service.SessionBindingModeDevice)
	ios := &service.SessionClient{Type: "ios", DeviceID: "6F1C2A7E"}
	pair, err := svc.GenerateTokenPair(bindingCtx("198.51.100.1", "6F1C2A7E", ios), user, "")
	require.NoError(t, err)

	// New network, same device: refresh works and the successor stays device-bound.
	next, err := svc.RefreshTokenPair(bindingCtx("203.0.113.9", "6F1C2A7E", nil), pair.RefreshToken)
	require.NoError(t, err)
	claims, err := svc.ValidateToken(next.AccessToken)
	require.NoError(t, err)
	require.Contains(t, claims.BindingHash, "d1:")

	// Another device: mismatch, family revoked.
	_, err = svc.RefreshTokenPair(bindingCtx("203.0.113.9", "OTHER", nil), next.RefreshToken)
	require.Equal(t, "SESSION_BINDING_MISMATCH", infraerrors.Reason(err))
}

func TestSessionBinding_WebSessionStaysStrict(t *testing.T) {
	svc, user := newBindingAuthService(t, service.SessionBindingModeDevice)
	pair, err := svc.GenerateTokenPair(bindingCtx("198.51.100.1", "", nil), user, "")
	require.NoError(t, err)
	claims, err := svc.ValidateToken(pair.AccessToken)
	require.NoError(t, err)
	require.NotContains(t, claims.BindingHash, ":")

	// Claiming a native client later cannot downgrade the Web session.
	_, err = svc.RefreshTokenPair(bindingCtx("203.0.113.9", "6F1C2A7E", &service.SessionClient{Type: "ios", DeviceID: "6F1C2A7E"}), pair.RefreshToken)
	require.Equal(t, "SESSION_BINDING_MISMATCH", infraerrors.Reason(err))
}
