//go:build unit

package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// effectiveStatusAuthCases are key states whose standard-mode gateway verdict must match APIKey.EffectiveStatus():
// the derived status shown to admins must not drift from what the auth middleware actually rejects.
func effectiveStatusAuthCases() []struct {
	name string
	key  service.APIKey
} {
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)
	return []struct {
		name string
		key  service.APIKey
	}{
		{"active", service.APIKey{Status: service.StatusAPIKeyActive}},
		{"inactive", service.APIKey{Status: service.StatusAPIKeyInactive}},
		{"legacy disabled", service.APIKey{Status: service.StatusAPIKeyDisabled}},
		{"unknown status", service.APIKey{Status: "banned"}},
		{"inactive expired and used up", service.APIKey{Status: service.StatusAPIKeyInactive, ExpiresAt: &past, Quota: 10, QuotaUsed: 10}},
		{"stored quota_exhausted with room left", service.APIKey{Status: service.StatusAPIKeyQuotaExhausted, Quota: 10, QuotaUsed: 1}},
		{"stored quota_exhausted and expired", service.APIKey{Status: service.StatusAPIKeyQuotaExhausted, ExpiresAt: &past}},
		{"stored expired with a future expiry", service.APIKey{Status: service.StatusAPIKeyExpired, ExpiresAt: &future}},
		{"stored expired and used up", service.APIKey{Status: service.StatusAPIKeyExpired, Quota: 10, QuotaUsed: 10}},
		{"active past expiry", service.APIKey{Status: service.StatusAPIKeyActive, ExpiresAt: &past}},
		{"active with a future expiry", service.APIKey{Status: service.StatusAPIKeyActive, ExpiresAt: &future}},
		{"active quota used up", service.APIKey{Status: service.StatusAPIKeyActive, Quota: 10, QuotaUsed: 10}},
		{"active unlimited quota", service.APIKey{Status: service.StatusAPIKeyActive, QuotaUsed: 100}},
		{"active expired and used up", service.APIKey{Status: service.StatusAPIKeyActive, ExpiresAt: &past, Quota: 10, QuotaUsed: 10}},
	}
}

func effectiveStatusAuthService(key service.APIKey, runMode string) (*service.APIKeyService, *config.Config, string) {
	user := &service.User{ID: 21, Role: service.RoleUser, Status: service.StatusActive, Balance: 10, Concurrency: 3}
	key.ID, key.UserID, key.Key = 4099, user.ID, "sk-effective-status"
	lookup := func(_ context.Context, raw string) (*service.APIKey, error) {
		if raw != key.Key {
			return nil, service.ErrAPIKeyNotFound
		}
		clone := key
		userClone := *user
		clone.User = &userClone
		return &clone, nil
	}
	cfg := &config.Config{RunMode: runMode}
	return service.NewAPIKeyService(&stubApiKeyRepo{getByKey: lookup}, nil, nil, nil, nil, nil, cfg), cfg, key.Key
}

// mainAuthVerdict maps the main auth middleware's answer for the key onto an effective_status value.
func mainAuthVerdict(t *testing.T, key service.APIKey, runMode string) string {
	t.Helper()
	svc, cfg, raw := effectiveStatusAuthService(key, runMode)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("x-api-key", raw)
	newAuthTestRouter(svc, nil, cfg).ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		return service.StatusAPIKeyActive
	}
	var resp ErrorResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), w.Body.String())
	switch resp.Code {
	case "API_KEY_DISABLED":
		return service.StatusAPIKeyInactive
	case "API_KEY_EXPIRED":
		return service.StatusAPIKeyExpired
	case "API_KEY_QUOTA_EXHAUSTED":
		return service.StatusAPIKeyQuotaExhausted
	}
	return fmt.Sprintf("unexpected %d %s", w.Code, w.Body.String())
}

// googleAuthVerdict maps the Gemini auth middleware's answer for the key onto an effective_status value.
func googleAuthVerdict(t *testing.T, key service.APIKey, runMode string) string {
	t.Helper()
	svc, cfg, raw := effectiveStatusAuthService(key, runMode)
	r := gin.New()
	r.Use(APIKeyAuthWithSubscriptionGoogle(svc, nil, cfg))
	r.GET("/v1beta/models", func(c *gin.Context) { c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1beta/models", nil)
	req.Header.Set("x-goog-api-key", raw)
	r.ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		return service.StatusAPIKeyActive
	}
	var resp struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), w.Body.String())
	switch resp.Error.Message {
	case "API key is disabled":
		return service.StatusAPIKeyInactive
	case "API key 已过期":
		return service.StatusAPIKeyExpired
	case "API key 额度已用完":
		return service.StatusAPIKeyQuotaExhausted
	}
	return fmt.Sprintf("unexpected %d %s", w.Code, w.Body.String())
}

func TestAPIKeyAuthRejectionMatchesEffectiveStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range effectiveStatusAuthCases() {
		t.Run(tc.name, func(t *testing.T) {
			key := tc.key
			require.Equal(t, key.EffectiveStatus(), mainAuthVerdict(t, tc.key, config.RunModeStandard))
		})
	}
}

func TestGoogleAPIKeyAuthRejectionMatchesEffectiveStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range effectiveStatusAuthCases() {
		t.Run(tc.name, func(t *testing.T) {
			key := tc.key
			require.Equal(t, key.EffectiveStatus(), googleAuthVerdict(t, tc.key, config.RunModeStandard))
		})
	}
}

// In RUN_MODE=simple both middlewares return before the expiry and quota checks, so only an inactive
// effective_status is rejected; expired / quota_exhausted keys still authenticate. EffectiveStatus documents that it
// reflects standard mode only, and clients use run_mode from /api/v1/admin/me to tell; this pins the gap.
func TestAPIKeyAuthSimpleModeRejectsOnlyInactiveEffectiveStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	verdicts := map[string]func(*testing.T, service.APIKey, string) string{
		"main":   mainAuthVerdict,
		"google": googleAuthVerdict,
	}
	for _, tc := range effectiveStatusAuthCases() {
		for name, verdict := range verdicts {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				key := tc.key
				want := service.StatusAPIKeyActive
				if key.EffectiveStatus() == service.StatusAPIKeyInactive {
					want = service.StatusAPIKeyInactive
				}
				require.Equal(t, want, verdict(t, tc.key, config.RunModeSimple))
			})
		}
	}
}
