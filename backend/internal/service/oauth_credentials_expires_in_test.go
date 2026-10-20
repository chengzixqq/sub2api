//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
)

func TestOpenAIBuildAccountCredentials_ExpiresInRequiresRefreshToken(t *testing.T) {
	svc := &OpenAIOAuthService{}
	expiresAt := time.Date(2026, 10, 27, 9, 40, 0, 0, time.UTC).Unix()

	withRefresh := svc.BuildAccountCredentials(&OpenAITokenInfo{
		AccessToken:  "at",
		RefreshToken: "rt",
		ExpiresIn:    864000,
		ExpiresAt:    expiresAt,
	})
	require.Equal(t, "864000", withRefresh["expires_in"])
	require.Equal(t, time.Unix(expiresAt, 0).Format(time.RFC3339), withRefresh["expires_at"])

	accessOnly := svc.BuildAccountCredentials(&OpenAITokenInfo{
		AccessToken: "at",
		ExpiresIn:   1234,
		ExpiresAt:   expiresAt,
	})
	require.NotContains(t, accessOnly, "expires_in")

	pat := svc.BuildAccountCredentials(&OpenAITokenInfo{
		AccessToken:  "pat",
		RefreshToken: "rt",
		ExpiresIn:    864000,
		ExpiresAt:    expiresAt,
		AuthMode:     OpenAIAuthModePersonalAccessToken,
	})
	require.NotContains(t, pat, "expires_in")

	zero := svc.BuildAccountCredentials(&OpenAITokenInfo{AccessToken: "at", RefreshToken: "rt"})
	require.NotContains(t, zero, "expires_in")
}

func TestGeminiAntigravityGrokBuildAccountCredentials_WriteExpiresIn(t *testing.T) {
	gemini := (&GeminiOAuthService{}).BuildAccountCredentials(&GeminiTokenInfo{
		AccessToken:  "at",
		RefreshToken: "rt",
		ExpiresIn:    3600,
		ExpiresAt:    1792215600,
	})
	require.Equal(t, "3600", gemini["expires_in"])
	require.Equal(t, "1792215600", gemini["expires_at"])
	require.Equal(t, "at", gemini["access_token"])

	geminiZero := (&GeminiOAuthService{}).BuildAccountCredentials(&GeminiTokenInfo{AccessToken: "at", ExpiresAt: 1792215600})
	require.NotContains(t, geminiZero, "expires_in")

	antigravity := (&AntigravityOAuthService{}).BuildAccountCredentials(&AntigravityTokenInfo{
		AccessToken:  "at",
		RefreshToken: "rt",
		ExpiresIn:    3600,
		ExpiresAt:    1792215600,
		ProjectID:    "proj",
	})
	require.Equal(t, "3600", antigravity["expires_in"])
	require.Equal(t, "1792215600", antigravity["expires_at"])
	require.Equal(t, "proj", antigravity["project_id"])

	antigravityZero := (&AntigravityOAuthService{}).BuildAccountCredentials(&AntigravityTokenInfo{AccessToken: "at"})
	require.NotContains(t, antigravityZero, "expires_in")

	grok := (&GrokOAuthService{}).BuildAccountCredentials(&GrokTokenInfo{
		AccessToken:  "at",
		RefreshToken: "rt",
		ExpiresIn:    3600,
		ExpiresAt:    1792215600,
	})
	require.Equal(t, "3600", grok["expires_in"])
	require.Equal(t, time.Unix(1792215600, 0).UTC().Format(time.RFC3339), grok["expires_at"])
	require.Equal(t, xai.DefaultCLIBaseURL, grok["base_url"])

	grokZero := (&GrokOAuthService{}).BuildAccountCredentials(&GrokTokenInfo{AccessToken: "at", ExpiresAt: 1792215600})
	require.NotContains(t, grokZero, "expires_in")
}
