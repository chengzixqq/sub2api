//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type fallbackSequenceUpstream struct {
	responses []*http.Response
	bodies    [][]byte
	calls     int
}

func (u *fallbackSequenceUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	if req != nil && req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		u.bodies = append(u.bodies, body)
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	if u.calls >= len(u.responses) {
		return nil, fmt.Errorf("unexpected upstream call %d", u.calls+1)
	}
	resp := u.responses[u.calls]
	u.calls++
	return resp, nil
}

func (u *fallbackSequenceUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

func fallbackNativeAccountForTest(passthrough bool) *Account {
	return &Account{
		ID:          905,
		Name:        "native-anthropic-apikey",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "upstream-key",
			"base_url": "https://api.anthropic.com",
		},
		Extra:       map[string]any{"anthropic_passthrough": passthrough},
		Status:      StatusActive,
		Schedulable: true,
	}
}

func fallbackNativeGatewayForTest(t *testing.T, upstream HTTPUpstream, cfg *config.Config) *GatewayService {
	t.Helper()
	if cfg == nil {
		cfg = &config.Config{}
	}
	settings := DefaultClaudeCustomizationSettings()
	raw, err := json.Marshal(settings)
	require.NoError(t, err)
	repo := &fallbackPolicySettingRepo{values: map[string]string{SettingKeyClaudeCustomization: string(raw)}}
	return &GatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
		httpUpstream:         upstream,
		rateLimitService:     &RateLimitService{},
		deferredService:      &DeferredService{},
		settingService:       NewSettingService(repo, cfg),
	}
}

func TestManagedAnthropicAPIKeyPath_DoesNotTurnUpstream400IntoLocalFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const upstreamJSON = `{"type":"error","error":{"type":"invalid_request_error","message":"thinking signature is invalid"},"request_id":"req_upstream"}`
	upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(upstreamJSON)),
	}}
	cfg := &config.Config{Gateway: config.GatewayConfig{FailoverOn400: true}}
	svc := &GatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
		httpUpstream:         upstream,
		rateLimitService:     &RateLimitService{},
		deferredService:      &DeferredService{},
	}
	account := &Account{
		ID:          905,
		Name:        "managed-anthropic-apikey",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "upstream-key",
			"base_url": "https://api.anthropic.com",
		},
		Status:      StatusActive,
		Schedulable: true,
	}
	body := []byte(`{"model":"claude-fable-5","max_tokens":200000,"messages":[{"role":"user","content":"hello"}]}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	result, err := svc.Forward(context.Background(), c, account, parsed)
	require.Nil(t, result)
	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr), "native API-key 400 must not trigger local account failover")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.JSONEq(t, upstreamJSON, rec.Body.String())
}

func TestNativeAnthropicAPIKeyPaths_PreserveValidThinkingWithoutRetry(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		passthrough := passthrough
		t.Run(fmt.Sprintf("automatic_passthrough_%t", passthrough), func(t *testing.T) {
			const upstreamJSON = `{"id":"msg_fable","type":"message","model":"claude-fable-5","content":[{"type":"thinking","thinking":"","signature":"valid-signature"},{"type":"text","text":"ok"}],"usage":{"input_tokens":10,"output_tokens":5,"iterations":[{"type":"message","model":"claude-fable-5"}]}}`
			upstream := &fallbackSequenceUpstream{responses: []*http.Response{{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(upstreamJSON)),
			}}}
			svc := fallbackNativeGatewayForTest(t, upstream, nil)
			body := []byte(`{"model":"claude-fable-5","max_tokens":200000,"fallbacks":"default","fallback_credit_token":"credit-token","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"prior","signature":"valid-signature"},{"type":"text","text":"prior answer"}]},{"role":"user","content":"continue"}]}`)
			parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			c.Request.Header.Set("anthropic-beta", "server-side-fallback-2025-11-01,fallback-credit-2025-11-01")

			result, err := svc.Forward(context.Background(), c, fallbackNativeAccountForTest(passthrough), parsed)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, 1, upstream.calls)
			require.Equal(t, "valid-signature", gjson.GetBytes(upstream.bodies[0], "messages.0.content.0.signature").String())
			require.Equal(t, int64(200000), gjson.GetBytes(upstream.bodies[0], "max_tokens").Int())
			require.True(t, gjson.GetBytes(upstream.bodies[0], "fallbacks").Exists())
			require.JSONEq(t, upstreamJSON, rec.Body.String())
			require.Equal(t, "claude-fable-5", result.UpstreamResponseModel)
		})
	}
}

func TestNativeAnthropicAPIKeyPaths_SignatureRetryPreservesUpstreamFallbackContract(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		passthrough := passthrough
		t.Run(fmt.Sprintf("automatic_passthrough_%t", passthrough), func(t *testing.T) {
			const signatureError = `{"type":"error","error":{"type":"invalid_request_error","message":"thinking signature is invalid"}}`
			const upstreamJSON = `{"id":"msg_opus","type":"message","model":"claude-opus-5","content":[{"type":"fallback","from":{"model":"claude-fable-5"},"to":{"model":"claude-opus-5"}},{"type":"text","text":"ok"}],"usage":{"input_tokens":10,"output_tokens":5,"iterations":[{"type":"fallback_message","model":"claude-opus-5"}]}}`
			upstream := &fallbackSequenceUpstream{responses: []*http.Response{
				{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(signatureError))},
				{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(upstreamJSON))},
			}}
			svc := fallbackNativeGatewayForTest(t, upstream, nil)
			body := []byte(`{"model":"claude-fable-5","max_tokens":200000,"fallbacks":["claude-opus-5"],"fallback_credit_token":"credit-token","thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"prior","signature":"bad-signature"},{"type":"text","text":"prior answer"}]},{"role":"user","content":"continue"}]}`)
			parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			c.Request.Header.Set("anthropic-beta", "server-side-fallback-2025-11-01,fallback-credit-2025-11-01")

			result, err := svc.Forward(context.Background(), c, fallbackNativeAccountForTest(passthrough), parsed)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.Equal(t, 2, upstream.calls, "signature errors permit exactly one same-account retry")
			require.Len(t, upstream.bodies, 2)
			require.Equal(t, int64(200000), gjson.GetBytes(upstream.bodies[1], "max_tokens").Int())
			require.Equal(t, "credit-token", gjson.GetBytes(upstream.bodies[1], "fallback_credit_token").String())
			require.Equal(t, "claude-opus-5", gjson.GetBytes(upstream.bodies[1], "fallbacks.0").String())
			require.JSONEq(t, upstreamJSON, rec.Body.String())
			require.Equal(t, "claude-opus-5", result.UpstreamResponseModel)
		})
	}
}

func TestNativeAnthropicAPIKeyPath_AccountOverrideDisablesSignatureRetry(t *testing.T) {
	const signatureError = `{"type":"error","error":{"type":"invalid_request_error","message":"thinking signature is invalid"}}`
	upstream := &fallbackSequenceUpstream{responses: []*http.Response{
		{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(signatureError))},
		{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"type":"message","model":"claude-opus-5","usage":{}}`))},
	}}
	svc := fallbackNativeGatewayForTest(t, upstream, nil)
	account := fallbackNativeAccountForTest(true)
	account.Extra[AccountClaudeCustomizationExtraKey] = map[string]any{
		"thinking_signature_retry_enabled": false,
	}
	body := []byte(`{"model":"claude-fable-5","fallbacks":"default","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"prior","signature":"bad-signature"}]}]}`)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	result, err := svc.Forward(context.Background(), c, account, parsed)
	require.Nil(t, result)
	require.Error(t, err)
	require.Equal(t, 1, upstream.calls, "account override must take precedence over the global retry setting")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.JSONEq(t, signatureError, rec.Body.String())
}

func TestNativeAnthropicAPIKeyPaths_ReturnMaxTokens400Unchanged(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		passthrough := passthrough
		t.Run(fmt.Sprintf("automatic_passthrough_%t", passthrough), func(t *testing.T) {
			const upstreamJSON = `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: 200000 > 128000, which is the maximum allowed number of output tokens for claude-fable-5"},"request_id":"req_cyber"}`
			upstream := &fallbackSequenceUpstream{responses: []*http.Response{{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(upstreamJSON)),
			}}}
			svc := fallbackNativeGatewayForTest(t, upstream, &config.Config{Gateway: config.GatewayConfig{FailoverOn400: true}})
			body := []byte(`{"model":"claude-fable-5","max_tokens":200000,"fallbacks":"default","messages":[{"role":"user","content":"hello"}]}`)
			parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

			result, err := svc.Forward(context.Background(), c, fallbackNativeAccountForTest(passthrough), parsed)
			require.Nil(t, result)
			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			require.False(t, errors.As(err, &failoverErr))
			require.Equal(t, 1, upstream.calls)
			require.Equal(t, int64(200000), gjson.GetBytes(upstream.bodies[0], "max_tokens").Int())
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.JSONEq(t, upstreamJSON, rec.Body.String())
		})
	}
}
