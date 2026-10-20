package dto

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func decodeJSONObject(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	return decoded
}

func TestAccountFromService_TokenStatusJSON(t *testing.T) {
	src := &service.Account{
		ID:       11,
		Name:     "claude",
		Platform: service.PlatformAnthropic,
		Type:     service.AccountTypeOAuth,
		Status:   service.StatusActive,
		Credentials: map[string]any{
			"access_token":        "secret-access",
			"refresh_token":       "secret-refresh",
			"expires_at":          "2026-10-17T17:40:00Z",
			"expires_in":          "28800",
			"_token_refreshed_at": "2026-10-17T09:40:01Z",
		},
	}

	detail := decodeJSONObject(t, AccountFromService(src))
	status, ok := detail["token_status"].(map[string]any)
	require.True(t, ok, "token_status must be an object for OAuth accounts")
	require.Equal(t, "2026-10-17T17:40:00Z", status["expires_at"])
	require.EqualValues(t, 28800, status["lifetime_seconds"])
	require.Equal(t, "2026-10-17T09:40:01Z", status["refreshed_at"])
	require.Equal(t, true, status["auto_refresh"])

	creds, ok := detail["credentials"].(map[string]any)
	require.True(t, ok)
	require.NotContains(t, creds, "access_token")
	require.NotContains(t, creds, "refresh_token")

	item := decodeJSONObject(t, AccountListItemFromAccount(AccountFromService(src)))
	itemStatus, ok := item["token_status"].(map[string]any)
	require.True(t, ok, "list items carry token_status for OAuth accounts")
	require.Equal(t, "2026-10-17T17:40:00Z", itemStatus["expires_at"])
	require.EqualValues(t, 28800, itemStatus["lifetime_seconds"])
}

func TestAccountFromService_TokenStatusNullFields(t *testing.T) {
	src := &service.Account{
		ID:          12,
		Name:        "gemini",
		Platform:    service.PlatformGemini,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Credentials: map[string]any{"access_token": "secret"},
	}

	status, ok := decodeJSONObject(t, AccountFromService(src))["token_status"].(map[string]any)
	require.True(t, ok)
	for _, key := range []string{"expires_at", "lifetime_seconds", "refreshed_at"} {
		require.Contains(t, status, key)
		require.Nil(t, status[key], key)
	}
	require.Equal(t, false, status["auto_refresh"])
}

func TestAccountFromService_TokenStatusNullForAPIKey(t *testing.T) {
	src := &service.Account{
		ID:          13,
		Name:        "apikey",
		Platform:    service.PlatformAnthropic,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Credentials: map[string]any{"api_key": "sk"},
	}

	detail := decodeJSONObject(t, AccountFromService(src))
	require.Contains(t, detail, "token_status")
	require.Nil(t, detail["token_status"])

	item := decodeJSONObject(t, AccountListItemFromAccount(AccountFromService(src)))
	require.NotContains(t, item, "token_status")
}

func TestAccountFromService_TokenStatusOutOfRangeTimesStillMarshal(t *testing.T) {
	src := &service.Account{
		ID:       14,
		Name:     "millis",
		Platform: service.PlatformAnthropic,
		Type:     service.AccountTypeSetupToken,
		Status:   service.StatusActive,
		Credentials: map[string]any{
			"access_token":   "secret",
			"expires_at":     "1792215600000",
			"_token_version": int64(1) << 62,
		},
	}

	for name, v := range map[string]any{
		"detail":    AccountFromService(src),
		"shallow":   AccountFromServiceShallow(src),
		"list item": AccountListItemFromAccount(AccountFromService(src)),
	} {
		t.Run(name, func(t *testing.T) {
			status, ok := decodeJSONObject(t, v)["token_status"].(map[string]any)
			require.True(t, ok)
			require.Nil(t, status["expires_at"])
			require.Nil(t, status["refreshed_at"])
		})
	}
}
