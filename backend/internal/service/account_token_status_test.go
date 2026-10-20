//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAccountTokenStatusFromAccount_ClaudeSetupTokenUnixSeconds(t *testing.T) {
	refreshedAt := time.Date(2026, 10, 17, 9, 40, 1, 0, time.UTC)
	account := &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeSetupToken,
		Credentials: map[string]any{
			"access_token":                "at",
			"refresh_token":               "rt",
			"expires_at":                  "1792215600",
			"expires_in":                  "28800",
			CredentialKeyTokenRefreshedAt: refreshedAt.Format(time.RFC3339),
		},
	}

	status := AccountTokenStatusFromAccount(account)

	require.NotNil(t, status)
	require.NotNil(t, status.ExpiresAt)
	require.Equal(t, time.Unix(1792215600, 0).UTC(), *status.ExpiresAt)
	require.Equal(t, time.UTC, status.ExpiresAt.Location())
	require.NotNil(t, status.LifetimeSeconds)
	require.Equal(t, int64(28800), *status.LifetimeSeconds)
	require.NotNil(t, status.RefreshedAt)
	require.True(t, refreshedAt.Equal(*status.RefreshedAt))
	require.True(t, status.AutoRefresh)

	numeric := &Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "rt",
			"expires_in":    float64(28800),
		},
	}
	numericStatus := AccountTokenStatusFromAccount(numeric)
	require.NotNil(t, numericStatus)
	require.NotNil(t, numericStatus.LifetimeSeconds)
	require.Equal(t, int64(28800), *numericStatus.LifetimeSeconds)
	require.Nil(t, numericStatus.ExpiresAt)
	require.Nil(t, numericStatus.RefreshedAt)
}

func TestAccountTokenStatusFromAccount_OpenAIRFC3339AndNumericExpiresAt(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "rt",
			"expires_at":    "2026-10-17T17:40:00Z",
		},
	}
	status := AccountTokenStatusFromAccount(account)
	require.NotNil(t, status)
	require.NotNil(t, status.ExpiresAt)
	require.Equal(t, time.Date(2026, 10, 17, 17, 40, 0, 0, time.UTC), *status.ExpiresAt)
	require.Nil(t, status.LifetimeSeconds)

	offset := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"expires_at": "2026-10-18T01:40:00+08:00",
		},
	}
	offsetStatus := AccountTokenStatusFromAccount(offset)
	require.NotNil(t, offsetStatus.ExpiresAt)
	require.Equal(t, time.Date(2026, 10, 17, 17, 40, 0, 0, time.UTC), *offsetStatus.ExpiresAt)
	require.Equal(t, time.UTC, offsetStatus.ExpiresAt.Location())

	numeric := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"expires_at": float64(1792215600),
		},
	}
	numericStatus := AccountTokenStatusFromAccount(numeric)
	require.NotNil(t, numericStatus.ExpiresAt)
	require.Equal(t, time.Unix(1792215600, 0).UTC(), *numericStatus.ExpiresAt)
}

func TestAccountTokenStatusFromAccount_RefreshedAtFallsBackToTokenVersion(t *testing.T) {
	version := time.Date(2026, 10, 17, 9, 40, 1, 987_000_000, time.UTC)
	want := time.Date(2026, 10, 17, 9, 40, 1, 0, time.UTC)

	for name, raw := range map[string]any{
		"int64":   version.UnixMilli(),
		"float64": float64(version.UnixMilli()),
	} {
		t.Run(name, func(t *testing.T) {
			status := AccountTokenStatusFromAccount(&Account{
				Platform:    PlatformGemini,
				Type:        AccountTypeOAuth,
				Credentials: map[string]any{"_token_version": raw},
			})
			require.NotNil(t, status.RefreshedAt)
			require.Equal(t, want, *status.RefreshedAt)
			require.Equal(t, time.UTC, status.RefreshedAt.Location())
		})
	}

	none := AccountTokenStatusFromAccount(&Account{Platform: PlatformGemini, Type: AccountTypeOAuth})
	require.NotNil(t, none)
	require.Nil(t, none.RefreshedAt)
	require.Nil(t, none.ExpiresAt)
	require.Nil(t, none.LifetimeSeconds)
	require.False(t, none.AutoRefresh)

	invalid := AccountTokenStatusFromAccount(&Account{
		Platform: PlatformGemini,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			CredentialKeyTokenRefreshedAt: "not-a-time",
			"_token_version":              version.UnixMilli(),
		},
	})
	require.NotNil(t, invalid.RefreshedAt)
	require.Equal(t, want, *invalid.RefreshedAt)

	preferred := AccountTokenStatusFromAccount(&Account{
		Platform: PlatformGemini,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			CredentialKeyTokenRefreshedAt: "2026-10-18T00:00:00Z",
			"_token_version":              version.UnixMilli(),
		},
	})
	require.NotNil(t, preferred.RefreshedAt)
	require.Equal(t, time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC), *preferred.RefreshedAt)
}

func TestAccountTokenStatusFromAccount_NilForNonOAuthAndShadow(t *testing.T) {
	require.Nil(t, AccountTokenStatusFromAccount(nil))
	require.Nil(t, AccountTokenStatusFromAccount(&Account{Platform: PlatformAnthropic, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk", "expires_at": "1792215600"}}))
	require.Nil(t, AccountTokenStatusFromAccount(&Account{Platform: PlatformAnthropic, Type: AccountTypeBedrock}))

	parentID := int64(9)
	require.Nil(t, AccountTokenStatusFromAccount(&Account{
		Platform:        PlatformOpenAI,
		Type:            AccountTypeOAuth,
		ParentAccountID: &parentID,
	}))

	require.NotNil(t, AccountTokenStatusFromAccount(&Account{Platform: PlatformAnthropic, Type: AccountTypeSetupToken}))
}

func TestAccountTokenStatusFromAccount_PersonalAccessTokenAndMissingRefreshToken(t *testing.T) {
	pat := AccountTokenStatusFromAccount(&Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "pat",
			"refresh_token": "stale",
			"auth_mode":     OpenAIAuthModePersonalAccessToken,
		},
	})
	require.NotNil(t, pat)
	require.False(t, pat.AutoRefresh)

	noRefresh := AccountTokenStatusFromAccount(&Account{
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "at", "refresh_token": "  "},
	})
	require.NotNil(t, noRefresh)
	require.False(t, noRefresh.AutoRefresh)
}

func TestAccountTokenStatusFromAccount_IgnoresInvalidExpiresIn(t *testing.T) {
	for name, raw := range map[string]any{
		"text":     "abc",
		"zero":     "0",
		"negative": float64(-5),
	} {
		t.Run(name, func(t *testing.T) {
			status := AccountTokenStatusFromAccount(&Account{
				Platform:    PlatformAnthropic,
				Type:        AccountTypeOAuth,
				Credentials: map[string]any{"expires_in": raw},
			})
			require.NotNil(t, status)
			require.Nil(t, status.LifetimeSeconds)
		})
	}
}

func TestStampTokenRefreshedAtOnNewAccessToken(t *testing.T) {
	now := time.Date(2026, 10, 17, 9, 40, 1, 500_000_000, time.FixedZone("CST", 8*3600))
	want := "2026-10-17T01:40:01Z"

	creds := map[string]any{"access_token": "new"}
	stampTokenRefreshedAtOnNewAccessToken(AccountTypeOAuth, "old", creds, now)
	require.Equal(t, want, creds[CredentialKeyTokenRefreshedAt])

	setup := map[string]any{"access_token": "new"}
	stampTokenRefreshedAtOnNewAccessToken(AccountTypeSetupToken, "", setup, now)
	require.Equal(t, want, setup[CredentialKeyTokenRefreshedAt])

	unchanged := map[string]any{"access_token": "same", CredentialKeyTokenRefreshedAt: "2026-01-01T00:00:00Z"}
	stampTokenRefreshedAtOnNewAccessToken(AccountTypeOAuth, "same", unchanged, now)
	require.Equal(t, "2026-01-01T00:00:00Z", unchanged[CredentialKeyTokenRefreshedAt])

	empty := map[string]any{"access_token": "  "}
	stampTokenRefreshedAtOnNewAccessToken(AccountTypeOAuth, "old", empty, now)
	require.NotContains(t, empty, CredentialKeyTokenRefreshedAt)

	apiKey := map[string]any{"access_token": "new", "api_key": "sk"}
	stampTokenRefreshedAtOnNewAccessToken(AccountTypeAPIKey, "old", apiKey, now)
	require.NotContains(t, apiKey, CredentialKeyTokenRefreshedAt)

	require.NotPanics(t, func() {
		stampTokenRefreshedAtOnNewAccessToken(AccountTypeOAuth, "old", nil, now)
	})
}
