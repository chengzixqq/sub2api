//go:build unit

package routes

import (
	"context"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type authAlignmentSettings struct {
	channelMonitorRouteSettingRepoStub
	settingsReads atomic.Int32
	delay         time.Duration
}

func (r *authAlignmentSettings) GetValue(ctx context.Context, key string) (string, error) {
	if key == service.SettingKeyClaudeCustomization {
		r.settingsReads.Add(1)
		time.Sleep(r.delay)
	}
	return r.channelMonitorRouteSettingRepoStub.GetValue(ctx, key)
}

func (r *authAlignmentSettings) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	return nil
}

func (r *authAlignmentSettings) ListClaudeAlignmentOverrides(context.Context, []string) ([]service.ClaudeAlignmentOverrides, error) {
	return []service.ClaudeAlignmentOverrides{{
		AccountID: 7, Platform: service.PlatformAnthropic, AccountType: service.AccountTypeAPIKey,
		Fields: map[string]any{service.ClaudeFieldRequestIDMode: service.ClaudeRequestIDFill},
	}}, nil
}

func TestOfficialCatalogAllowlistNotFound(t *testing.T) {
	repo := &authAlignmentSettings{channelMonitorRouteSettingRepoStub: channelMonitorRouteSettingRepoStub{
		values: map[string]string{service.SettingKeyClaudeCustomization: `{"models_catalog":"official"}`},
	}}
	router := newGatewayRoutesTestRouterWithGroupSettings(allowlistGroup(service.PlatformAnthropic, true, "visible"), service.NewSettingService(repo, nil))
	for _, path := range []string{"/models/claude-nope-1", "/v1/models/claude-nope-1"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("x-api-key", "test")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		id := w.Result().Header.Get("Request-Id")
		require.Regexp(t, "^req_011C[1-9A-HJ-NP-Za-km-z]{20}$", id)
		require.Equal(t, http.StatusNotFound, w.Code)
		require.Equal(t, "application/json", w.Result().Header.Get("Content-Type"))
		require.Equal(t, `{"type":"error","error":{"type":"not_found_error","message":"model: claude-nope-1"},"request_id":"`+id+`"}`, w.Body.String())
	}
}

func TestOfficialCatalogAllowlistNonClaudePlatformsUnchanged(t *testing.T) {
	for _, platform := range []string{service.PlatformOpenAI, service.PlatformGrok} {
		for _, path := range []string{"/v1/models/blocked", "/models/blocked"} {
			t.Run(platform+path, func(t *testing.T) {
				request := func(policy string) *httptest.ResponseRecorder {
					repo := &authAlignmentSettings{channelMonitorRouteSettingRepoStub: channelMonitorRouteSettingRepoStub{
						values: map[string]string{service.SettingKeyClaudeCustomization: policy},
					}}
					router := newGatewayRoutesTestRouterWithGroupSettings(allowlistGroup(platform, true, "visible"), service.NewSettingService(repo, nil))
					req := httptest.NewRequest(http.MethodGet, path, nil)
					req.Header.Set("x-api-key", "test")
					rec := httptest.NewRecorder()
					router.ServeHTTP(rec, req)
					return rec
				}
				base := request(`{"models_catalog":"synthetic","request_id_mode":"passthrough"}`)
				got := request(`{"models_catalog":"official","request_id_mode":"fill"}`)
				require.Equal(t, http.StatusNotFound, base.Code)
				require.Equal(t, base.Code, got.Code)
				baseHeader, gotHeader := base.Result().Header.Clone(), got.Result().Header.Clone()
				// The route generates a new correlation ID for each request.
				require.NotEmpty(t, baseHeader.Get("X-Client-Request-Id"))
				require.NotEmpty(t, gotHeader.Get("X-Client-Request-Id"))
				baseHeader.Del("X-Client-Request-Id")
				gotHeader.Del("X-Client-Request-Id")
				require.Equal(t, baseHeader, gotHeader)
				require.Equal(t, base.Body.Bytes(), got.Body.Bytes())
			})
		}
	}
}

func TestRequestIDFill_AuthFailureGlobalOnly(t *testing.T) {
	synctest.Test(t, checkWarmAuthFailureRoutes)
}

func checkWarmAuthFailureRoutes(t *testing.T) {
	cfg := &config.Config{RunMode: config.RunModeStandard}
	keys := service.NewAPIKeyService(&keyBillingRouteAPIKeyRepo{}, nil, nil, nil, nil, nil, cfg)
	for _, mode := range []string{"passthrough", "fill"} {
		repo := &authAlignmentSettings{channelMonitorRouteSettingRepoStub: channelMonitorRouteSettingRepoStub{
			values: map[string]string{service.SettingKeyClaudeCustomization: `{"request_id_mode":"` + mode + `"}`},
		}}
		settings := service.NewSettingService(repo, cfg)
		settings.AnyAlignmentReject(t.Context(), service.ClaudeFieldUnknownBetaHeader)
		r := gin.New()
		RegisterGatewayRoutes(r, &handler.Handlers{}, middleware.NewAPIKeyAuthMiddlewareWithSettings(keys, nil, cfg, settings), keys, nil, nil, settings, nil, cfg)
		for _, path := range []string{"/v1/messages", "/v1/models", "/v1/chat/completions"} {
			method := http.MethodPost
			if path == "/v1/models" {
				method = http.MethodGet
			}
			req := httptest.NewRequest(method, path, nil)
			req.Header.Set("anthropic-version", "2023-06-01")
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			require.Equal(t, 401, rec.Code)
			id := rec.Result().Header.Get("request-id")
			if mode == "fill" && path != "/v1/chat/completions" {
				require.Regexp(t, "^req_011C[1-9A-HJ-NP-Za-km-z]{20}$", id)
				require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
				require.Equal(t, `{"type":"error","error":{"type":"authentication_error","message":"x-api-key header is required"},"request_id":"`+id+`"}`, rec.Body.String())
			} else {
				require.Empty(t, id)
				require.False(t, gjson.Get(rec.Body.String(), "request_id").Exists())
			}
		}
		require.EqualValues(t, 1, repo.settingsReads.Load(), "auth must reuse the warm alignment snapshot")
	}
}

func TestOfficialAuthColdAndInvalidation(t *testing.T) {
	cfg := &config.Config{RunMode: config.RunModeStandard}
	keys := service.NewAPIKeyService(&keyBillingRouteAPIKeyRepo{}, nil, nil, nil, nil, nil, cfg)
	repo := &authAlignmentSettings{delay: 300 * time.Millisecond, channelMonitorRouteSettingRepoStub: channelMonitorRouteSettingRepoStub{values: map[string]string{}}}
	settings := service.NewSettingService(repo, cfg)
	r := gin.New()
	RegisterGatewayRoutes(r, &handler.Handlers{}, middleware.NewAPIKeyAuthMiddlewareWithSettings(keys, nil, cfg, settings), keys, nil, nil, settings, nil, cfg)
	request := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages", nil))
		return w
	}
	type result struct {
		w       *httptest.ResponseRecorder
		elapsed time.Duration
	}
	const concurrency = 8
	start := make(chan struct{})
	done := make(chan result, concurrency)
	for range concurrency {
		go func() {
			<-start
			now := time.Now()
			w := request()
			done <- result{w, time.Since(now)}
		}()
	}
	close(start)
	var longest time.Duration
	for range concurrency {
		result := <-done
		longest = max(longest, result.elapsed)
		require.Equal(t, 401, result.w.Code)
		require.Empty(t, result.w.Result().Header.Get("Request-Id"))
		require.Equal(t, `{"type":"error","error":{"type":"authentication_error","message":"x-api-key header is required"}}`, result.w.Body.String())
	}
	t.Logf("max cold auth latency over %d requests: %v", concurrency, longest)
	require.Less(t, longest, 50*time.Millisecond)
	require.Eventually(t, func() bool { return repo.settingsReads.Load() == 1 }, time.Second, time.Millisecond)
	settings.AnyAlignmentReject(t.Context(), service.ClaudeFieldUnknownBetaHeader) // join the initial refresh
	require.EqualValues(t, 1, repo.settingsReads.Load())
	policy := service.DefaultClaudeCustomizationSettings()
	policy.RequestIDMode = service.ClaudeRequestIDFill
	require.NoError(t, settings.SetClaudeCustomizationSettings(t.Context(), policy))
	require.Eventually(t, func() bool {
		w := request()
		id := w.Result().Header.Get("Request-Id")
		return w.Code == 401 && id != "" && gjson.Get(w.Body.String(), "request_id").String() == id
	}, 2*time.Second, 5*time.Millisecond)
	require.EqualValues(t, 2, repo.settingsReads.Load())
}

func TestRegisterGatewayRoutesAPIKeyAuthErrorFormat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{RunMode: config.RunModeStandard}
	repo := &keyBillingRouteAPIKeyRepo{apiKey: &service.APIKey{
		ID: 1, UserID: 2, Key: "expired", Status: service.StatusAPIKeyExpired,
		User: &service.User{ID: 2, Status: service.StatusActive, Balance: 10},
	}}
	svc := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
	r := gin.New()
	RegisterGatewayRoutes(r, &handler.Handlers{}, middleware.NewAPIKeyAuthMiddleware(svc, nil, cfg), svc, nil, nil, nil, nil, cfg)

	const (
		anthropic401        = `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`
		anthropicMissing401 = `{"type":"error","error":{"type":"authentication_error","message":"x-api-key header is required"}}`
		anthropic403        = `{"type":"error","error":{"type":"permission_error","message":"API key has expired"}}`
		legacy401           = `{"code":"INVALID_API_KEY","message":"Invalid API key","reason":"INVALID_API_KEY"}`
		missing401          = `{"code":"API_KEY_REQUIRED","message":"API key is required in Authorization header (Bearer scheme), x-api-key header, or x-goog-api-key header","reason":"API_KEY_REQUIRED"}`
		legacy403           = `{"code":"API_KEY_EXPIRED","message":"API key 已过期","reason":"API_KEY_EXPIRED"}`
		google401           = `{"error":{"code":401,"status":"UNAUTHENTICATED","message":"Invalid API key"}}`
	)
	for _, tc := range []struct {
		name, method, path, apiKey, authorization, version string
		status                                             int
		body                                               string
	}{
		{"messages invalid", "POST", "/v1/messages", "wrong", "", "", 401, anthropic401},
		{"messages missing", "POST", "/v1/messages", "", "", "", 401, anthropicMissing401},
		{"messages expired", "POST", "/v1/messages", "expired", "", "", 403, anthropic403},
		{"messages query key", "POST", "/v1/messages?key=wrong", "", "", "", 400, `{"type":"error","error":{"type":"invalid_request_error","message":"API keys in query parameters are not supported. Use the x-api-key header."}}`},
		{"count tokens", "POST", "/v1/messages/count_tokens", "", "Bearer wrong", "", 401, anthropic401},
		{"count tokens alias", "POST", "/messages/count_tokens", "", "", "", 401, anthropicMissing401},
		{"antigravity", "POST", "/antigravity/v1/messages", "", "", "", 401, anthropicMissing401},
		{"antigravity count", "POST", "/antigravity/v1/messages/count_tokens", "expired", "", "", 403, anthropic403},
		{"models x api key", "GET", "/v1/models", "wrong", "", "", 401, anthropic401},
		{"models version only", "GET", "/v1/models", "", "", "2023-06-01", 401, anthropicMissing401},
		{"models no auth", "GET", "/v1/models", "", "", "", 401, missing401},
		{"models bearer", "GET", "/v1/models", "", "Bearer wrong", "", 401, legacy401},
		{"models bearer wins", "GET", "/v1/models", "expired", "bEaReR wrong", "", 401, legacy401},
		{"models version wins", "GET", "/v1/models", "expired", "Bearer wrong", "2023-06-01", 401, anthropic401},
		{"models empty bearer falls back", "GET", "/v1/models", "expired", "Bearer ", "", 403, anthropic403},
		{"models basic falls back", "GET", "/v1/models", "expired", "Basic ignored", "", 403, anthropic403},
		{"models alias", "GET", "/models", "expired", "", "", 403, anthropic403},
		{"models alias bearer", "GET", "/models", "", "Bearer wrong", "", 401, legacy401},
		{"chat invalid", "POST", "/v1/chat/completions", "wrong", "", "2023-06-01", 401, legacy401},
		{"chat expired", "POST", "/v1/chat/completions", "expired", "", "", 403, legacy403},
		{"responses", "POST", "/v1/responses", "wrong", "", "", 401, legacy401},
		{"usage", "GET", "/v1/usage", "wrong", "", "2023-06-01", 401, legacy401},
		{"single model", "GET", "/v1/models/messages", "wrong", "", "2023-06-01", 401, legacy401},
		{"codex models", "GET", "/backend-api/codex/models", "wrong", "", "2023-06-01", 401, legacy401},
		{"antigravity models", "GET", "/antigravity/v1/models", "wrong", "", "", 401, legacy401},
		{"gemini", "GET", "/v1beta/models", "", "Bearer wrong", "2023-06-01", 401, google401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.apiKey != "" {
				req.Header.Set("x-api-key", tc.apiKey)
			}
			if tc.authorization != "" {
				req.Header.Set("Authorization", tc.authorization)
			}
			if tc.version != "" {
				req.Header.Set("anthropic-version", tc.version)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, tc.status, w.Code, w.Body.String())
			contentType, _, err := mime.ParseMediaType(w.Header().Get("Content-Type"))
			require.NoError(t, err)
			require.Equal(t, "application/json", contentType)
			if strings.HasPrefix(tc.body, `{"type":"error"`) {
				require.Equal(t, tc.body, w.Body.String())
				require.Equal(t, "application/json", w.Header().Get("Content-Type"))
			} else {
				require.JSONEq(t, tc.body, w.Body.String())
			}
		})
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/messages", nil))
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestDisabledClaudeKeyAuthBytes(t *testing.T) {
	cfg := &config.Config{RunMode: config.RunModeStandard}
	repo := &keyBillingRouteAPIKeyRepo{apiKey: &service.APIKey{ID: 1, Key: "disabled", Status: service.StatusDisabled, User: &service.User{ID: 2, Status: service.StatusActive}}}
	keys := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
	r := gin.New()
	RegisterGatewayRoutes(r, &handler.Handlers{}, middleware.NewAPIKeyAuthMiddleware(keys, nil, cfg), keys, nil, nil, nil, nil, cfg)
	req := httptest.NewRequest("POST", "/v1/messages", nil)
	req.Header.Set("x-api-key", "disabled")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, 401, w.Code)
	require.Equal(t, "application/json", w.Header().Get("Content-Type"))
	require.Equal(t, `{"type":"error","error":{"type":"authentication_error","message":"API key is disabled"}}`, w.Body.String())
}
