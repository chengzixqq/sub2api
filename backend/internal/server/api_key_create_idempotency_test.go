//go:build unit

package server_test

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// The Web "my API keys" page creates keys with POST /api/v1/keys and never sends an Idempotency-Key
// (frontend/src/api/keys.ts). Creating a key must keep working once an operator switches
// idempotency.observe_only off.
func TestUserAPIKeyCreate_KeylessSucceedsWhenIdempotencyKeysAreEnforced(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := service.DefaultIdempotencyConfig()
	cfg.ObserveOnly = false
	// A keyless request never touches the store, so no repository is needed to prove it is not rejected.
	service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(nil, cfg))
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })

	deps := newContractDeps(t)
	headers := map[string]string{"Content-Type": "application/json"}
	for i, body := range []string{
		`{"name":"Key One","custom_key":"sk_custom_enforced_1"}`,
		`{"name":"Key Two","custom_key":"sk_custom_enforced_2"}`,
	} {
		status, resp := doRequest(t, deps.router, http.MethodPost, "/api/v1/keys", body, headers)
		require.Equal(t, http.StatusOK, status, resp)
		require.NotContains(t, resp, "IDEMPOTENCY_KEY_REQUIRED")
		require.Len(t, deps.apiKeyRepo.byID, i+1, "each keyless create runs")
	}
}
