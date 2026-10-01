//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAnthropicPassthroughNonSignature400ClosesTransportBody(t *testing.T) {
	original := &observedAnthropicStreamBody{ReadCloser: io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens exceeds maximum"}}`))}
	upstream := &fallbackSequenceUpstream{responses: []*http.Response{{StatusCode: 400, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: original}}}
	svc := fallbackNativeGatewayForTest(t, upstream, nil)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"claude-fable-5","max_tokens":200000,"messages":[{"role":"user","content":"hello"}]}`)), PlatformAnthropic)
	require.NoError(t, err)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	_, err = svc.Forward(context.Background(), c, fallbackNativeAccountForTest(true), parsed)
	require.Error(t, err)
	require.EqualValues(t, 1, original.closeCount.Load(), "original transport body was replaced without Close")
}

func TestAlphaSearchFailoverClosesTransportBody(t *testing.T) {
	for _, pat := range []bool{false, true} {
		name := "direct"
		if pat {
			name = "PAT fallback"
		}
		t.Run(name, func(t *testing.T) {
			original := &observedAnthropicStreamBody{ReadCloser: io.NopCloser(strings.NewReader(`{"error":{"message":"Unauthorized"}}`))}
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 401, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: original}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test"}}
			if pat {
				account.Type = AccountTypeOAuth
				account.Credentials = map[string]any{"access_token": "at-test", "auth_mode": OpenAIAuthModePersonalAccessToken}
			}
			body := []byte(`{"model":"gpt-5.6-sol","commands":{"search_query":[{"q":"news"}]}}`)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", nil)
			_, err := svc.ForwardAlphaSearch(context.Background(), c, account, body)
			var failover *UpstreamFailoverError
			require.ErrorAs(t, err, &failover)
			require.EqualValues(t, 1, original.closeCount.Load())
		})
	}
}

func TestGrokErrorClosesTransportBody(t *testing.T) {
	original := &observedAnthropicStreamBody{ReadCloser: io.NopCloser(strings.NewReader(`{"error":{"message":"temporary"}}`))}
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 503, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: original}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	account := &Account{ID: 42, Platform: PlatformGrok, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test"}}
	body := []byte(`{"model":"grok","input":"hello","stream":false}`)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	_, err := svc.forwardGrokResponses(context.Background(), c, account, body, "grok", false, time.Now())
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.EqualValues(t, 1, original.closeCount.Load())
}

func TestAnthropicPassthroughSignatureRetryUsesRetryError(t *testing.T) {
	const firstError = `{"type":"error","error":{"type":"invalid_request_error","message":"thinking signature is invalid"}}`
	const retryError = `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens exceeds maximum"}}`
	upstream := &fallbackSequenceUpstream{responses: []*http.Response{
		{StatusCode: 400, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(firstError))},
		{StatusCode: 400, Header: http.Header{"Content-Type": []string{"application/json"}, "X-Request-Id": []string{"retry"}}, Body: io.NopCloser(strings.NewReader(retryError))},
	}}
	svc := fallbackNativeGatewayForTest(t, upstream, nil)
	parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"claude-fable-5","max_tokens":200000,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"prior","signature":"bad"}]},{"role":"user","content":"hello"}]}`)), PlatformAnthropic)
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	_, err = svc.Forward(context.Background(), c, fallbackNativeAccountForTest(true), parsed)
	require.Error(t, err)
	require.Equal(t, 2, upstream.calls)
	require.JSONEq(t, retryError, rec.Body.String())
}
