//go:build unit

package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type fallbackPolicySettingRepo struct {
	values map[string]string
}

func (r *fallbackPolicySettingRepo) Get(context.Context, string) (*Setting, error) {
	return nil, ErrSettingNotFound
}
func (r *fallbackPolicySettingRepo) GetValue(_ context.Context, key string) (string, error) {
	if value, ok := r.values[key]; ok {
		return value, nil
	}
	return "", ErrSettingNotFound
}
func (r *fallbackPolicySettingRepo) Set(_ context.Context, key, value string) error {
	if r.values == nil {
		r.values = make(map[string]string)
	}
	r.values[key] = value
	return nil
}
func (r *fallbackPolicySettingRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *fallbackPolicySettingRepo) SetMultiple(context.Context, map[string]string) error { return nil }
func (r *fallbackPolicySettingRepo) GetAll(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *fallbackPolicySettingRepo) Delete(context.Context, string) error { return nil }

// TestNativeAnthropicAPIKeyPaths_PreserveUpstreamFallbackRequest locks the
// upstream-authoritative contract for both API-key paths. The managed path is
// used when automatic passthrough is disabled; the dedicated passthrough
// builder is used when it is enabled. Neither path may reinterpret or trim
// the upstream fallback request.
func TestNativeAnthropicAPIKeyPaths_PreserveUpstreamFallbackRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const betaHeader = claude.BetaServerSideFallback + "," + claude.BetaFallbackCredit + ",x-future-fallback-beta"
	body := []byte(`{"model":"claude-fable-5","max_tokens":200000,"fallbacks":["claude-opus-4-6","claude-sonnet-4-6"],"fallback_credit_token":"credit-token","messages":[{"role":"user","content":"hello"}]}`)

	tests := []struct {
		name            string
		autoPassthrough bool
		build           func(*GatewayService, *gin.Context, *Account, []byte) (*http.Request, []byte, error)
	}{
		{
			name:            "managed API-key path",
			autoPassthrough: false,
			build: func(s *GatewayService, c *gin.Context, account *Account, body []byte) (*http.Request, []byte, error) {
				return s.buildUpstreamRequest(context.Background(), c, account, body, "upstream-key", "apikey", "claude-fable-5", false, false)
			},
		},
		{
			name:            "automatic passthrough path",
			autoPassthrough: true,
			build: func(s *GatewayService, c *gin.Context, account *Account, body []byte) (*http.Request, []byte, error) {
				return s.buildUpstreamRequestAnthropicAPIKeyPassthrough(context.Background(), c, account, body, "upstream-key")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			c.Request.Header.Set("anthropic-beta", betaHeader)

			account := &Account{
				ID:       901,
				Platform: PlatformAnthropic,
				Type:     AccountTypeAPIKey,
				Credentials: map[string]any{
					"api_key": "upstream-key",
				},
				Extra: map[string]any{
					"anthropic_passthrough": tt.autoPassthrough,
				},
			}
			svc := &GatewayService{cfg: &config.Config{}}

			req, wireBody, err := tt.build(svc, c, account, body)
			require.NoError(t, err)
			require.NotNil(t, req)
			require.JSONEq(t, string(body), string(wireBody), "wire body must preserve fallback request fields")

			outBody, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			require.JSONEq(t, string(body), string(outBody), "request body must match the returned wire body")
			require.Equal(t, int64(200000), gjson.GetBytes(outBody, "max_tokens").Int(), "Fable max_tokens must not be clamped")
			require.Equal(t, "credit-token", gjson.GetBytes(outBody, "fallback_credit_token").String())
			fallbacks := gjson.GetBytes(outBody, "fallbacks").Array()
			require.Len(t, fallbacks, 2)
			require.Equal(t, "claude-opus-4-6", fallbacks[0].String())
			require.Equal(t, "claude-sonnet-4-6", fallbacks[1].String())
			require.Equal(t, betaHeader, getHeaderRaw(req.Header, "anthropic-beta"), "client beta order and unknown tokens must be preserved")
		})
	}
}

// TestNativeAnthropicAPIKeyPaths_DoNotInventFallbackBeta locks the negative
// part of passthrough semantics: a client-declared fallback body is retained
// even when the client did not send a fallback beta. The gateway must not
// inject a beta token or silently delete the upstream trigger.
func TestNativeAnthropicAPIKeyPaths_DoNotInventFallbackBeta(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const betaHeader = "x-client-experimental-beta"
	body := []byte(`{"model":"claude-fable-5-1","max_tokens":200000,"fallbacks":"default","fallback_credit_token":"credit-token","messages":[]}`)

	tests := []struct {
		name  string
		build func(*GatewayService, *gin.Context, *Account, []byte) (*http.Request, []byte, error)
	}{
		{
			name: "managed API-key path",
			build: func(s *GatewayService, c *gin.Context, account *Account, body []byte) (*http.Request, []byte, error) {
				return s.buildUpstreamRequest(context.Background(), c, account, body, "upstream-key", "apikey", "claude-fable-5-1", false, false)
			},
		},
		{
			name: "automatic passthrough path",
			build: func(s *GatewayService, c *gin.Context, account *Account, body []byte) (*http.Request, []byte, error) {
				return s.buildUpstreamRequestAnthropicAPIKeyPassthrough(context.Background(), c, account, body, "upstream-key")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			c.Request.Header.Set("anthropic-beta", betaHeader)

			account := &Account{
				ID:          902,
				Platform:    PlatformAnthropic,
				Type:        AccountTypeAPIKey,
				Credentials: map[string]any{"api_key": "upstream-key"},
				Extra:       map[string]any{"anthropic_passthrough": tt.name == "automatic passthrough path"},
			}
			svc := &GatewayService{cfg: &config.Config{}}

			req, wireBody, err := tt.build(svc, c, account, body)
			require.NoError(t, err)
			require.JSONEq(t, string(body), string(wireBody))
			require.Equal(t, betaHeader, getHeaderRaw(req.Header, "anthropic-beta"), "gateway must not invent fallback beta")
			require.True(t, gjson.GetBytes(wireBody, "fallbacks").Exists())
			require.True(t, gjson.GetBytes(wireBody, "fallback_credit_token").Exists())
			require.Equal(t, int64(200000), gjson.GetBytes(wireBody, "max_tokens").Int())
		})
	}
}

func TestManagedAnthropicAPIKeyPath_PreservesFallbackBetaAgainstGenericFilterRule(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	global := DefaultClaudeCustomizationSettings()
	globalRaw, err := json.Marshal(global)
	require.NoError(t, err)
	betaRaw, err := json.Marshal(BetaPolicySettings{Rules: []BetaPolicyRule{
		{BetaToken: claude.BetaServerSideFallback, Action: BetaPolicyActionFilter, Scope: BetaPolicyScopeAll},
		{BetaToken: claude.BetaFallbackCredit, Action: BetaPolicyActionFilter, Scope: BetaPolicyScopeAll},
	}})
	require.NoError(t, err)
	repo := &fallbackPolicySettingRepo{values: map[string]string{
		SettingKeyClaudeCustomization: string(globalRaw),
		SettingKeyBetaPolicySettings:  string(betaRaw),
	}}

	account := &Account{
		ID:          903,
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "upstream-key"},
	}
	svc := &GatewayService{
		cfg:            &config.Config{},
		settingService: NewSettingService(repo, &config.Config{}),
	}
	c.Request.Header.Set("anthropic-beta", claude.BetaServerSideFallback+","+claude.BetaFallbackCredit+",x-future-beta")
	body := []byte(`{"model":"claude-fable-5","max_tokens":200000,"fallbacks":"default","fallback_credit_token":"credit-token","messages":[]}`)

	req, wireBody, err := svc.buildUpstreamRequest(context.Background(), c, account, body, "upstream-key", "apikey", "claude-fable-5", false, false)
	require.NoError(t, err)
	require.JSONEq(t, string(body), string(wireBody))
	require.Equal(t, claude.BetaServerSideFallback+","+claude.BetaFallbackCredit+",x-future-beta", getHeaderRaw(req.Header, "anthropic-beta"),
		"native API-key forwarding must override generic filter rules for upstream fallback betas")
	require.True(t, gjson.GetBytes(wireBody, "fallbacks").Exists())
	require.True(t, gjson.GetBytes(wireBody, "fallback_credit_token").Exists())
	require.Equal(t, int64(200000), gjson.GetBytes(wireBody, "max_tokens").Int())
}
