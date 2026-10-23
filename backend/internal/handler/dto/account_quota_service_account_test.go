package dto

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// service_account 与 apikey 同语义：设了总额度才输出 quota_limit / quota_used。
func TestAccountFromServiceShallow_ServiceAccountQuota(t *testing.T) {
	t.Run("no limit omits quota fields", func(t *testing.T) {
		out := AccountFromServiceShallow(&service.Account{
			ID:       1,
			Platform: service.PlatformGemini,
			Type:     service.AccountTypeServiceAccount,
			Extra:    map[string]any{"quota_used": 12.5},
		})
		require.Nil(t, out.QuotaUsed)
		require.Nil(t, out.QuotaLimit)
		require.Nil(t, out.QuotaDailyLimit)
		require.Nil(t, out.QuotaWeeklyLimit)

		payload, err := json.Marshal(out)
		require.NoError(t, err)
		var decoded map[string]any
		require.NoError(t, json.Unmarshal(payload, &decoded))
		require.NotContains(t, decoded, "quota_used")
		require.NotContains(t, decoded, "quota_limit")
	})

	t.Run("with limits outputs limits and usage", func(t *testing.T) {
		out := AccountFromServiceShallow(&service.Account{
			ID:       3,
			Platform: service.PlatformGemini,
			Type:     service.AccountTypeServiceAccount,
			Extra: map[string]any{
				"quota_used":        30.0,
				"quota_limit":       100.0,
				"quota_daily_limit": 10.0,
			},
		})
		require.NotNil(t, out.QuotaLimit)
		require.InDelta(t, 100.0, *out.QuotaLimit, 1e-9)
		require.NotNil(t, out.QuotaUsed)
		require.InDelta(t, 30.0, *out.QuotaUsed, 1e-9)
		require.NotNil(t, out.QuotaDailyLimit)
		require.NotNil(t, out.QuotaDailyUsed)
	})

	t.Run("lite list item keeps quota fields", func(t *testing.T) {
		item := AccountListItemFromAccount(AccountFromServiceShallow(&service.Account{
			ID:       4,
			Platform: service.PlatformGemini,
			Type:     service.AccountTypeServiceAccount,
			Extra:    map[string]any{"quota_used": 7.25, "quota_limit": 290.0},
		}))
		payload, err := json.Marshal(item)
		require.NoError(t, err)
		var decoded map[string]any
		require.NoError(t, json.Unmarshal(payload, &decoded))
		require.InDelta(t, 7.25, decoded["quota_used"], 1e-9)
		require.InDelta(t, 290.0, decoded["quota_limit"], 1e-9)
	})
}

// apikey 维持原语义：未设总额度时不输出 quota_used。
func TestAccountFromServiceShallow_APIKeyWithoutLimitOmitsQuotaUsed(t *testing.T) {
	out := AccountFromServiceShallow(&service.Account{
		ID:    5,
		Type:  service.AccountTypeAPIKey,
		Extra: map[string]any{"quota_used": 5.0},
	})
	require.Nil(t, out.QuotaUsed)
	require.Nil(t, out.QuotaLimit)
}

func TestAccountFromServiceShallow_ServiceAccountQuotaPreservesAppFields(t *testing.T) {
	errorAt := time.Date(2026, 10, 24, 1, 2, 3, 0, time.UTC)
	until := errorAt.Add(time.Hour)
	account := service.Account{
		ID: 81, Name: "vertex-app", Platform: service.PlatformGemini,
		Type: service.AccountTypeServiceAccount, Status: service.StatusError,
		ErrorMessage: "service account token request returned 400: account not found",
		ErrorAt:      &errorAt, TempUnschedulableUntil: &until,
		RateLimitResetAt: &until, Schedulable: false,
	}
	baseline := AccountFromServiceShallow(&account)
	account.Extra = map[string]any{
		"quota_limit": 100.0, "quota_used": 12.5,
		"quota_daily_limit": 20.0, "quota_daily_used": 3.0,
		"quota_weekly_limit": 60.0, "quota_weekly_used": 8.0,
	}
	extended := AccountFromServiceShallow(&account)
	for _, tc := range []struct {
		name   string
		before any
		after  any
	}{
		{"detail", baseline, extended},
		{"list", AccountListItemFromAccount(baseline), AccountListItemFromAccount(extended)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decode := func(value any) map[string]any {
				payload, err := json.Marshal(value)
				require.NoError(t, err)
				var decoded map[string]any
				require.NoError(t, json.Unmarshal(payload, &decoded))
				return decoded
			}
			before, after := decode(tc.before), decode(tc.after)
			for _, key := range []string{"id", "type", "platform", "status", "error_message", "error_at", "temp_unschedulable_until", "rate_limit_reset_at"} {
				require.Contains(t, before, key)
				require.Equal(t, before[key], after[key], "App contract field %s", key)
			}
			// Detail responses include null token_status; compact list responses
			// omit it for service accounts. Quota support must preserve both forms.
			beforeToken, beforeHasToken := before["token_status"]
			afterToken, afterHasToken := after["token_status"]
			require.Equal(t, beforeHasToken, afterHasToken)
			require.Equal(t, beforeToken, afterToken)
			require.InDelta(t, 100.0, after["quota_limit"], 1e-9)
			require.InDelta(t, 12.5, after["quota_used"], 1e-9)
			require.InDelta(t, 20.0, after["quota_daily_limit"], 1e-9)
			require.InDelta(t, 60.0, after["quota_weekly_limit"], 1e-9)
			// Only extra and the existing optional quota fields may differ.
			delete(before, "extra")
			delete(after, "extra")
			for key := range after {
				if strings.HasPrefix(key, "quota_") {
					delete(before, key)
					delete(after, key)
				}
			}
			require.Equal(t, before, after)
		})
	}
}
