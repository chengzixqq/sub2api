package service

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// pushMemSettingRepo is an in-memory SettingRepository.
type pushMemSettingRepo struct {
	mu     sync.Mutex
	values map[string]string
}

func (r *pushMemSettingRepo) Get(ctx context.Context, key string) (*Setting, error) {
	v, err := r.GetValue(ctx, key)
	if err != nil {
		return nil, err
	}
	return &Setting{Key: key, Value: v}, nil
}

func (r *pushMemSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return v, nil
}

func (r *pushMemSettingRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values == nil {
		r.values = map[string]string{}
	}
	r.values[key] = value
	return nil
}

func (r *pushMemSettingRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *pushMemSettingRepo) SetMultiple(context.Context, map[string]string) error { return nil }
func (r *pushMemSettingRepo) GetAll(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *pushMemSettingRepo) Delete(context.Context, string) error { return nil }

func pushTestP8(t *testing.T, curve elliptic.Curve) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(curve, rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func newPushConfigServiceForTest(keyConfigured bool) (*PushConfigService, *pushMemSettingRepo) {
	repo := &pushMemSettingRepo{}
	cfg := &config.Config{}
	cfg.Totp.EncryptionKeyConfigured = keyConfigured
	return NewPushConfigService(repo, pushHexEncryptor{}, cfg), repo
}

func pushStrPtr(s string) *string { return &s }

func TestPushConfigService_UpdateRequiresFixedKeyAndValidP8(t *testing.T) {
	ctx := WithScope(context.Background(), AdminScope())
	p8 := pushTestP8(t, elliptic.P256())

	noKey, _ := newPushConfigServiceForTest(false)
	_, err := noKey.Update(ctx, PushAPNsConfigUpdate{PrivateKey: pushStrPtr(p8)})
	require.Equal(t, "SECRET_ENCRYPTION_KEY_NOT_CONFIGURED", infraerrors.Reason(err))

	svc, repo := newPushConfigServiceForTest(true)
	_, err = svc.Update(ctx, PushAPNsConfigUpdate{PrivateKey: pushStrPtr(pushTestP8(t, elliptic.P384()))})
	require.Equal(t, "INVALID_APNS_CONFIG", infraerrors.Reason(err), "APNs keys are P-256")
	_, err = svc.Update(ctx, PushAPNsConfigUpdate{PrivateKey: pushStrPtr("not a pem")})
	require.Equal(t, "INVALID_APNS_CONFIG", infraerrors.Reason(err))
	_, err = svc.Update(ctx, PushAPNsConfigUpdate{TeamID: pushStrPtr("short")})
	require.Equal(t, "INVALID_APNS_CONFIG", infraerrors.Reason(err))
	_, err = svc.Update(ctx, PushAPNsConfigUpdate{Environment: pushStrPtr("staging")})
	require.Equal(t, "INVALID_APNS_CONFIG", infraerrors.Reason(err))
	_, err = svc.Update(ctx, PushAPNsConfigUpdate{Enabled: boolPtr(true), TeamID: pushStrPtr("TEAM123456")})
	require.Equal(t, "INVALID_APNS_CONFIG", infraerrors.Reason(err), "enabling needs team, key id and private key")

	view, err := svc.Update(ctx, PushAPNsConfigUpdate{
		Enabled: boolPtr(true), TeamID: pushStrPtr("team123456"), KeyID: pushStrPtr("KEY1234567"),
		PrivateKey: pushStrPtr(p8), Topic: pushStrPtr("com.example.sub2mobile"),
	})
	require.NoError(t, err)
	require.True(t, view.Enabled)
	require.Equal(t, "TEAM123456", view.TeamID)
	require.Equal(t, "production", view.Environment)
	require.True(t, view.PrivateKeyConfigured)
	require.True(t, view.Ready)
	stored := repo.values[SettingKeyPushAPNsConfig]
	require.NotContains(t, stored, "PRIVATE KEY", "the p8 key is stored encrypted")

	rt, err := svc.Resolve(ctx)
	require.NoError(t, err)
	require.Equal(t, "TEAM123456", rt.TeamID)
	require.NotNil(t, rt.PrivateKey)

	view, err = svc.Update(ctx, PushAPNsConfigUpdate{Environment: pushStrPtr("sandbox"), PrivateKey: pushStrPtr("")})
	require.NoError(t, err)
	require.True(t, view.PrivateKeyConfigured, "an empty private key keeps the stored one")
	rt, err = svc.Resolve(ctx)
	require.NoError(t, err)
	require.Equal(t, "sandbox", rt.Environment, "Update invalidates the cached runtime config")

	got, err := svc.Get(ctx)
	require.NoError(t, err)
	require.True(t, got.PrivateKeyConfigured)
	require.True(t, got.EncryptionKeyConfigured)

	_, err = svc.Update(ctx, PushAPNsConfigUpdate{Enabled: boolPtr(false)})
	require.NoError(t, err)
	_, err = svc.Resolve(ctx)
	require.ErrorIs(t, err, ErrPushNotConfigured)

	vendor := WithScope(context.Background(), VendorScope(7, WorkspacePermissions{}))
	_, err = svc.Get(vendor)
	require.ErrorIs(t, err, domain.ErrWorkspacePermissionDenied)
	_, err = svc.Update(vendor, PushAPNsConfigUpdate{Enabled: boolPtr(false)})
	require.ErrorIs(t, err, domain.ErrWorkspacePermissionDenied)
}

func TestPushConfigService_GetDefaultsAndUndecryptableKey(t *testing.T) {
	ctx := WithScope(context.Background(), AdminScope())
	svc, repo := newPushConfigServiceForTest(true)
	view, err := svc.Get(ctx)
	require.NoError(t, err)
	require.False(t, view.Enabled)
	require.False(t, view.PrivateKeyConfigured)
	require.Equal(t, "production", view.Environment)
	_, err = svc.Resolve(ctx)
	require.ErrorIs(t, err, ErrPushNotConfigured)

	repo.values = map[string]string{SettingKeyPushAPNsConfig: `{"apns_enabled":true,"apns_team_id":"TEAM123456","apns_key_id":"KEY1234567","apns_private_key":"garbage"}`}
	svc.Invalidate()
	_, err = svc.Resolve(ctx)
	require.ErrorIs(t, err, ErrPushNotConfigured, "a key encrypted with another secret cannot be used")
	require.False(t, strings.Contains(err.Error(), "garbage"))
}
