//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func requireFreshTokenRefreshedAt(t *testing.T, creds map[string]any, before time.Time) {
	t.Helper()
	raw, ok := creds[CredentialKeyTokenRefreshedAt].(string)
	require.True(t, ok, "credentials must carry %s", CredentialKeyTokenRefreshedAt)
	stamped, err := time.Parse(time.RFC3339, raw)
	require.NoError(t, err)
	require.False(t, stamped.Before(before.Truncate(time.Second)))
	require.WithinDuration(t, time.Now(), stamped, 2*time.Second)
}

func TestUpdateAccount_StampsTokenRefreshedAtWhenAccessTokenChanges(t *testing.T) {
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
		t.Run(accountType, func(t *testing.T) {
			repo := &updateAccountCredsRepoStub{account: &Account{
				ID:       301,
				Platform: PlatformAnthropic,
				Type:     accountType,
				Status:   StatusActive,
				Credentials: map[string]any{
					"access_token":                "at-old",
					"refresh_token":               "rt-old",
					CredentialKeyTokenRefreshedAt: "2026-01-01T00:00:00Z",
				},
			}}
			svc := &adminServiceImpl{accountRepo: repo}
			before := time.Now()

			_, err := svc.UpdateAccount(context.Background(), 301, &UpdateAccountInput{
				Credentials: map[string]any{
					"access_token":                "at-new",
					"refresh_token":               "rt-new",
					CredentialKeyTokenRefreshedAt: "2026-01-01T00:00:00Z",
				},
			})

			require.NoError(t, err)
			require.Equal(t, "at-new", repo.account.Credentials["access_token"])
			requireFreshTokenRefreshedAt(t, repo.account.Credentials, before)
		})
	}

	t.Run("apply switches apikey to oauth", func(t *testing.T) {
		repo := &updateAccountCredsRepoStub{account: &Account{
			ID:          302,
			Platform:    PlatformAnthropic,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Credentials: map[string]any{"api_key": "sk-old"},
		}}
		svc := &adminServiceImpl{accountRepo: repo}
		before := time.Now()

		_, err := svc.UpdateAccount(context.Background(), 302, &UpdateAccountInput{
			Type:        AccountTypeOAuth,
			Credentials: map[string]any{"access_token": "at-new", "refresh_token": "rt-new"},
		})

		require.NoError(t, err)
		requireFreshTokenRefreshedAt(t, repo.account.Credentials, before)
	})
}

func TestUpdateAccount_DoesNotStampWhenAccessTokenUnchangedOrAPIKey(t *testing.T) {
	t.Run("full PUT without tokens keeps the requested value", func(t *testing.T) {
		repo := &updateAccountCredsRepoStub{account: &Account{
			ID:       303,
			Platform: PlatformAnthropic,
			Type:     AccountTypeOAuth,
			Status:   StatusActive,
			Credentials: map[string]any{
				"access_token":                "at-existing",
				"refresh_token":               "rt-existing",
				"base_url":                    "https://old.example.com",
				CredentialKeyTokenRefreshedAt: "2026-01-01T00:00:00Z",
			},
		}}
		svc := &adminServiceImpl{accountRepo: repo}

		_, err := svc.UpdateAccount(context.Background(), 303, &UpdateAccountInput{
			Credentials: map[string]any{
				"base_url":                    "https://new.example.com",
				CredentialKeyTokenRefreshedAt: "2026-01-01T00:00:00Z",
			},
		})

		require.NoError(t, err)
		require.Equal(t, "at-existing", repo.account.Credentials["access_token"])
		require.Equal(t, "2026-01-01T00:00:00Z", repo.account.Credentials[CredentialKeyTokenRefreshedAt])
	})

	t.Run("same access token is not a refresh", func(t *testing.T) {
		repo := &updateAccountCredsRepoStub{account: &Account{
			ID:          304,
			Platform:    PlatformOpenAI,
			Type:        AccountTypeOAuth,
			Status:      StatusActive,
			Credentials: map[string]any{"access_token": "pat"},
		}}
		svc := &adminServiceImpl{accountRepo: repo}

		_, err := svc.UpdateAccount(context.Background(), 304, &UpdateAccountInput{
			Credentials: map[string]any{"access_token": "pat", "plan_type": "plus"},
		})

		require.NoError(t, err)
		require.NotContains(t, repo.account.Credentials, CredentialKeyTokenRefreshedAt)
	})

	t.Run("apikey accounts are never stamped", func(t *testing.T) {
		repo := &updateAccountCredsRepoStub{account: &Account{
			ID:          305,
			Platform:    PlatformAnthropic,
			Type:        AccountTypeAPIKey,
			Status:      StatusActive,
			Credentials: map[string]any{"api_key": "sk-old"},
		}}
		svc := &adminServiceImpl{accountRepo: repo}

		_, err := svc.UpdateAccount(context.Background(), 305, &UpdateAccountInput{
			Credentials: map[string]any{"api_key": "sk-new", "access_token": "odd"},
		})

		require.NoError(t, err)
		require.NotContains(t, repo.account.Credentials, CredentialKeyTokenRefreshedAt)
	})
}

func TestCreateAccount_OAuthStampsTokenRefreshedAt(t *testing.T) {
	repo := &upstreamBillingProbeAccountRepo{}
	svc := &adminServiceImpl{accountRepo: repo}
	ctx := WithScope(context.Background(), AdminScope())
	before := time.Now()

	created, err := svc.CreateAccount(ctx, &CreateAccountInput{
		Name:                 "claude-oauth",
		Platform:             PlatformAnthropic,
		Type:                 AccountTypeOAuth,
		SkipDefaultGroupBind: true,
		Credentials:          map[string]any{"access_token": "at", "refresh_token": "rt"},
	})
	require.NoError(t, err)
	requireFreshTokenRefreshedAt(t, created.Credentials, before)

	imported, err := svc.CreateAccount(ctx, &CreateAccountInput{
		Name:                 "imported-setup-token",
		Platform:             PlatformAnthropic,
		Type:                 AccountTypeSetupToken,
		SkipDefaultGroupBind: true,
		Credentials: map[string]any{
			"access_token":                "at",
			CredentialKeyTokenRefreshedAt: "2026-10-01T08:00:00Z",
		},
	})
	require.NoError(t, err)
	require.Equal(t, "2026-10-01T08:00:00Z", imported.Credentials[CredentialKeyTokenRefreshedAt],
		"an import that already carries a valid refresh time keeps it")

	legacyRefreshedAt := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	legacyExport, err := svc.CreateAccount(ctx, &CreateAccountInput{
		Name:                 "imported-legacy-oauth",
		Platform:             PlatformAnthropic,
		Type:                 AccountTypeOAuth,
		SkipDefaultGroupBind: true,
		Credentials: map[string]any{
			"access_token":   "at",
			"refresh_token":  "rt",
			"_token_version": float64(legacyRefreshedAt.UnixMilli()),
		},
	})
	require.NoError(t, err)
	require.NotContains(t, legacyExport.Credentials, CredentialKeyTokenRefreshedAt,
		"an export from an instance predating _token_refreshed_at keeps its _token_version fallback")
	legacyStatus := AccountTokenStatusFromAccount(legacyExport)
	require.NotNil(t, legacyStatus.RefreshedAt)
	require.Equal(t, legacyRefreshedAt, *legacyStatus.RefreshedAt)

	apiKey, err := svc.CreateAccount(ctx, &CreateAccountInput{
		Name:                 "claude-apikey",
		Platform:             PlatformAnthropic,
		Type:                 AccountTypeAPIKey,
		SkipDefaultGroupBind: true,
		Credentials:          map[string]any{"api_key": "sk"},
	})
	require.NoError(t, err)
	require.NotContains(t, apiKey.Credentials, CredentialKeyTokenRefreshedAt)
}
