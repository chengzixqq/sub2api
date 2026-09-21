//go:build unit

package service

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDebugLogGatewaySnapshot_RedactsFallbackCredentialToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway-debug.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	require.NoError(t, err)

	svc := &GatewayService{}
	svc.debugGatewayBodyFile.Store(f)
	svc.debugLogGatewaySnapshot(
		"UPSTREAM_FORWARD",
		http.Header{"anthropic-beta": []string{"server-side-fallback"}},
		[]byte(`{"model":"claude-fable-5","fallbacks":"default","fallback_credit_token":"credit-secret","messages":[]}`),
		nil,
	)
	require.NoError(t, f.Close())

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(got), `"fallback_credit_token": "***"`)
	require.NotContains(t, string(got), "credit-secret")
}
