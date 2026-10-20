//go:build unit

package repository

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"github.com/Wei-Shaw/sub2api/internal/pkg/oauth"
	"github.com/stretchr/testify/require"
)

func TestGeminiOAuthClient_ExchangeCodeNon2xxReturnsTypedError(t *testing.T) {
	t.Setenv(geminicli.GeminiCLIOAuthClientSecretEnv, "test-built-in-secret")
	srv := newLocalTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()

	client := &geminiOAuthClient{tokenURL: srv.URL, cfg: &config.Config{}}
	_, err := client.ExchangeCode(context.Background(), "code_assist", "code", "verifier", geminicli.GeminiCLIRedirectURI, "")
	require.Error(t, err)

	var exchangeErr *oauth.TokenExchangeError
	require.True(t, errors.As(err, &exchangeErr), "expected *oauth.TokenExchangeError, got %T", err)
	require.Equal(t, http.StatusBadRequest, exchangeErr.UpstreamStatusCode())
	require.True(t, strings.HasPrefix(err.Error(), "token exchange failed: status 400, body: "), err.Error())
	require.Contains(t, err.Error(), "invalid_grant")
}
