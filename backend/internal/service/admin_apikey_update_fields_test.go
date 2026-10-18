//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func aksStr(v string) *string      { return &v }
func aksFloat(v float64) *float64  { return &v }
func aksStrs(v []string) *[]string { return &v }

func TestAdminUpdateAPIKeyFields_DisableReportsPrevious(t *testing.T) {
	repo := &apiKeyRepoStub{apiKey: &APIKey{ID: 4087, UserID: 1, Key: "sk-live", Name: "脚本", Status: StatusActive}}
	cache := &authCacheInvalidatorStub{}
	svc := &adminServiceImpl{apiKeyRepo: repo, authCacheInvalidator: cache}

	res, err := svc.AdminUpdateAPIKeyFields(adminCtx(), 4087, AdminUpdateAPIKeyInput{Status: aksStr(StatusAPIKeyInactive)})
	require.NoError(t, err)
	require.Equal(t, StatusAPIKeyInactive, res.APIKey.Status)
	require.Equal(t, map[string]any{"status": StatusActive}, res.Previous)
	require.Len(t, repo.updatedKeys, 1)
	require.Equal(t, []string{"sk-live"}, cache.keys)
}

func TestAdminUpdateAPIKeyFields_AllFields(t *testing.T) {
	expires := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	repo := &apiKeyRepoStub{apiKey: &APIKey{ID: 1, Key: "sk", Name: "old", Status: StatusAPIKeyQuotaExhausted, Quota: 10, QuotaUsed: 10, RateLimit1d: 5}}
	svc := &adminServiceImpl{apiKeyRepo: repo}

	res, err := svc.AdminUpdateAPIKeyFields(adminCtx(), 1, AdminUpdateAPIKeyInput{
		Name:        aksStr("<b>新</b>"),
		Quota:       aksFloat(100),
		ExpiresAt:   &expires,
		IPBlacklist: aksStrs([]string{"203.0.113.9"}),
		RateLimit1d: aksFloat(30),
	})
	require.NoError(t, err)
	k := res.APIKey
	require.Equal(t, "&lt;b&gt;新&lt;/b&gt;", k.Name, "names are escaped like the user path")
	require.Equal(t, StatusActive, k.Status, "raising the quota reactivates an exhausted key")
	require.Equal(t, 100.0, k.Quota)
	require.True(t, expires.Equal(*k.ExpiresAt))
	require.Equal(t, []string{"203.0.113.9"}, k.IPBlacklist)
	require.Equal(t, 30.0, k.RateLimit1d)
	require.Equal(t, "old", res.Previous["name"])
	require.Equal(t, 10.0, res.Previous["quota"])
	require.Equal(t, 5.0, res.Previous["rate_limit_1d"])
	require.Equal(t, StatusAPIKeyQuotaExhausted, res.Previous["status"], "an implied status change is reported too")
}

func TestAdminUpdateAPIKeyFields_ClearExpirationReactivates(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	repo := &apiKeyRepoStub{apiKey: &APIKey{ID: 1, Key: "sk", Status: StatusAPIKeyExpired, ExpiresAt: &past}}
	svc := &adminServiceImpl{apiKeyRepo: repo}

	res, err := svc.AdminUpdateAPIKeyFields(adminCtx(), 1, AdminUpdateAPIKeyInput{ClearExpiration: true})
	require.NoError(t, err)
	require.Nil(t, res.APIKey.ExpiresAt)
	require.Equal(t, StatusActive, res.APIKey.Status)
	require.Contains(t, res.Previous, "expires_at")
}

func TestAdminUpdateAPIKeyFields_Validation(t *testing.T) {
	repo := &apiKeyRepoStub{apiKey: &APIKey{ID: 1, Key: "sk", Status: StatusActive}}
	svc := &adminServiceImpl{apiKeyRepo: repo}

	_, err := svc.AdminUpdateAPIKeyFields(adminCtx(), 1, AdminUpdateAPIKeyInput{Status: aksStr(StatusAPIKeyQuotaExhausted)})
	require.Equal(t, "INVALID_API_KEY_STATUS", infraerrors.Reason(err), "only active / inactive can be set")
	_, err = svc.AdminUpdateAPIKeyFields(adminCtx(), 1, AdminUpdateAPIKeyInput{Quota: aksFloat(-1)})
	require.Equal(t, "API_KEY_LIMIT_INVALID", infraerrors.Reason(err))
	_, err = svc.AdminUpdateAPIKeyFields(adminCtx(), 1, AdminUpdateAPIKeyInput{IPWhitelist: aksStrs([]string{"not-an-ip"})})
	require.True(t, errors.Is(err, ErrInvalidIPPattern))
	_, err = svc.AdminUpdateAPIKeyFields(adminCtx(), 1, AdminUpdateAPIKeyInput{Name: aksStr("   ")})
	require.Equal(t, "API_KEY_NAME_REQUIRED", infraerrors.Reason(err))
	require.Empty(t, repo.updatedKeys, "invalid input must not be written")
}

func TestAdminUpdateAPIKeyFields_NothingToChange(t *testing.T) {
	repo := &apiKeyRepoStub{apiKey: &APIKey{ID: 1, Key: "sk", Status: StatusActive}}
	svc := &adminServiceImpl{apiKeyRepo: repo}

	res, err := svc.AdminUpdateAPIKeyFields(adminCtx(), 1, AdminUpdateAPIKeyInput{})
	require.NoError(t, err)
	require.Empty(t, res.Previous)
	require.Empty(t, repo.updatedKeys)
}

func TestAdminUpdateAPIKeyFields_VendorScopeIsRefused(t *testing.T) {
	repo := &apiKeyRepoStub{apiKey: &APIKey{ID: 1, Key: "sk", Status: StatusActive}}
	svc := &adminServiceImpl{apiKeyRepo: repo}
	vendor := WithScope(context.Background(), VendorScope(3, WorkspacePermissions{AccountManage: true}))

	_, err := svc.AdminUpdateAPIKeyFields(vendor, 1, AdminUpdateAPIKeyInput{Status: aksStr(StatusAPIKeyInactive)})
	require.ErrorIs(t, err, domain.ErrWorkspaceScopeViolation)
}
