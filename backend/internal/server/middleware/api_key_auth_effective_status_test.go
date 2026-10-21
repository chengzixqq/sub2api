//go:build unit

package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// effectiveStatusAuthCases are key states whose gateway verdict must match APIKey.EffectiveStatus(): the derived
// status shown to admins must not drift from what the auth middleware actually rejects.
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

func effectiveStatusAuthService(key service.APIKey) (*service.APIKeyService, *config.Config, string) {
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
	cfg := &config.Config{RunMode: config.RunModeStandard}
	return service.NewAPIKeyService(&stubApiKeyRepo{getByKey: lookup}, nil, nil, nil, nil, nil, cfg), cfg, key.Key
}

func TestAPIKeyAuthRejectionMatchesEffectiveStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range effectiveStatusAuthCases() {
		t.Run(tc.name, func(t *testing.T) {
			svc, cfg, raw := effectiveStatusAuthService(tc.key)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			req.Header.Set("x-api-key", raw)
			newAuthTestRouter(svc, nil, cfg).ServeHTTP(w, req)

			got := "unexpected"
			if w.Code == http.StatusOK {
				got = service.StatusAPIKeyActive
			} else {
				var resp ErrorResponse
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), w.Body.String())
				switch resp.Code {
				case "API_KEY_DISABLED":
					got = service.StatusAPIKeyInactive
				case "API_KEY_EXPIRED":
					got = service.StatusAPIKeyExpired
				case "API_KEY_QUOTA_EXHAUSTED":
					got = service.StatusAPIKeyQuotaExhausted
				}
			}
			key := tc.key
			require.Equal(t, key.EffectiveStatus(), got, "%d %s", w.Code, w.Body.String())
		})
	}
}

func TestGoogleAPIKeyAuthRejectionMatchesEffectiveStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	verdicts := map[string]string{
		"API key is disabled": service.StatusAPIKeyInactive,
		"API key 已过期":         service.StatusAPIKeyExpired,
		"API key 额度已用完":       service.StatusAPIKeyQuotaExhausted,
	}
	for _, tc := range effectiveStatusAuthCases() {
		t.Run(tc.name, func(t *testing.T) {
			svc, cfg, raw := effectiveStatusAuthService(tc.key)
			r := gin.New()
			r.Use(APIKeyAuthWithSubscriptionGoogle(svc, nil, cfg))
			r.GET("/v1beta/models", func(c *gin.Context) { c.Status(http.StatusOK) })
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/v1beta/models", nil)
			req.Header.Set("x-goog-api-key", raw)
			r.ServeHTTP(w, req)

			got := "unexpected"
			if w.Code == http.StatusOK {
				got = service.StatusAPIKeyActive
			} else {
				var resp struct {
					Error struct {
						Message string `json:"message"`
					} `json:"error"`
				}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), w.Body.String())
				if v, ok := verdicts[resp.Error.Message]; ok {
					got = v
				}
			}
			key := tc.key
			require.Equal(t, key.EffectiveStatus(), got, "%d %s", w.Code, w.Body.String())
		})
	}
}
