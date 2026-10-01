//go:build unit

package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type retryLogErrorUpstream struct {
	responses []*http.Response
	calls     int
}

func TestClaudeHTTPErrorLogsHonorBodySettingAndRedactSecrets(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "body_disabled"
		if enabled {
			name = "body_enabled"
		}
		t.Run(name, func(t *testing.T) {
			sink, restore := captureStructuredLog(t)
			defer restore()
			upstream := &fallbackSequenceUpstream{responses: []*http.Response{{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"invalid_request_error","message":"invalid option"},"debug":"body-marker","api_key":"body-api-secret","access_token":"body-token-secret","endpoint":"https://body-private.example/v1/messages?key=body-query-secret"}`)),
			}}}
			svc := fallbackNativeGatewayForTest(t, upstream, &config.Config{Gateway: config.GatewayConfig{LogUpstreamErrorBody: enabled}})
			account := fallbackNativeAccountForTest(false)
			account.Extra[AccountClaudeCustomizationExtraKey] = map[string]any{"url_redaction_enabled": true}
			parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`)), PlatformAnthropic)
			require.NoError(t, err)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

			_, err = svc.Forward(context.Background(), c, account, parsed)
			require.Error(t, err)
			require.True(t, sink.ContainsMessage("Upstream error"))
			require.Equal(t, enabled, sink.ContainsMessage("body-marker"), "body diagnostics must follow the explicit setting")
			for _, secret := range []string{"body-api-secret", "body-token-secret", "body-query-secret", "body-private.example"} {
				require.False(t, sink.ContainsMessage(secret), "sensitive diagnostic field: %s", secret)
			}
		})
	}
}

func (u *retryLogErrorUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	if req.Body != nil {
		_ = req.Body.Close()
	}
	u.calls++
	if u.calls <= len(u.responses) {
		return u.responses[u.calls-1], nil
	}
	return nil, errors.New(`Post "https://retry-private.example/v1/messages?key=retry-secret": connection reset`)
}

func (u *retryLogErrorUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func TestClaudeRetryTransportLogsRespectURLRedaction(t *testing.T) {
	for _, tc := range []struct {
		name     string
		messages []string
		log      string
	}{
		{"signature", []string{"thinking signature is invalid"}, "signature error retry failed"},
		{"tool_signature", []string{"thinking signature is invalid", "tool_use signature is invalid"}, "tool-downgrade signature retry failed"},
		{"budget", []string{"thinking.budget_tokens must be >= 1024"}, "budget rectifier retry failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink, restore := captureStructuredLog(t)
			defer restore()
			upstream := &retryLogErrorUpstream{}
			for _, message := range tc.messages {
				upstream.responses = append(upstream.responses, &http.Response{
					StatusCode: http.StatusBadRequest,
					Header:     http.Header{"Content-Type": {"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"invalid_request_error","message":"` + message + `"}}`)),
				})
			}
			svc := fallbackNativeGatewayForTest(t, upstream, nil)
			account := fallbackNativeAccountForTest(false)
			account.Extra[AccountClaudeCustomizationExtraKey] = map[string]any{
				"url_redaction_enabled":                 true,
				"thinking_tool_downgrade_retry_enabled": true,
			}
			body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":2048,"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"prior","signature":"bad-signature"},{"type":"tool_use","id":"tool-1","name":"lookup","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"result"}]}]}`)
			parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
			require.NoError(t, err)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

			_, err = svc.Forward(context.Background(), c, account, parsed)
			require.Error(t, err)
			require.Equal(t, len(tc.messages)+1, upstream.calls)
			require.True(t, sink.ContainsMessage(tc.log), "the retry failure must remain observable")
			require.False(t, sink.ContainsMessage("retry-private.example"), "request URL redaction must cover retry logs")
			require.False(t, sink.ContainsMessage("retry-secret"), "request credentials must not appear in retry logs")
			require.True(t, sink.ContainsMessage("connection reset"), "redaction must retain the failure reason")
		})
	}
}

func TestClaudeSSEAndRetryExhaustedLogsUseBodyRedactionPolicy(t *testing.T) {
	const diagnostic = `{"type":"error","error":{"type":"invalid_request_error","message":"invalid option"},"debug":"body-marker","api_key":"body-api-secret","access_token":"body-token-secret","endpoint":"https://body-private.example/v1/messages?key=body-query-secret"}`
	for _, path := range []string{"sse_error", "retry_exhausted"} {
		for _, enabled := range []bool{false, true} {
			name := path + "/body_disabled"
			if enabled {
				name = path + "/body_enabled"
			}
			t.Run(name, func(t *testing.T) {
				sink, restore := captureStructuredLog(t)
				defer restore()
				cfg := &config.Config{Gateway: config.GatewayConfig{LogUpstreamErrorBody: enabled}}
				svc := fallbackNativeGatewayForTest(t, nil, cfg)
				account := fallbackNativeAccountForTest(false)
				account.Extra[AccountClaudeCustomizationExtraKey] = map[string]any{"url_redaction_enabled": true}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
				var err error
				if path == "sse_error" {
					svc.httpUpstream = &fallbackSequenceUpstream{responses: []*http.Response{{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("event: error\ndata: " + diagnostic + "\n\n"))}}}
					var parsed *ParsedRequest
					parsed, err = ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","stream":true,"max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`)), PlatformAnthropic)
					require.NoError(t, err)
					_, err = svc.Forward(context.Background(), c, account, parsed)
					require.True(t, sink.ContainsMessage("SSE error event in stream"))
				} else {
					c.Set(redactUpstreamURLContextKey, true)
					resp := &http.Response{StatusCode: http.StatusForbidden, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(diagnostic))}
					_, err = svc.handleRetryExhaustedError(context.Background(), resp, c, account)
					require.True(t, sink.ContainsMessage("upstream error 403 after"))
				}
				require.Error(t, err)
				require.Equal(t, enabled, sink.ContainsMessage("body-marker"))
				for _, secret := range []string{"body-api-secret", "body-token-secret", "body-query-secret", "body-private.example"} {
					require.False(t, sink.ContainsMessage(secret), "diagnostic leaked %s", secret)
				}
			})
		}
	}
}
