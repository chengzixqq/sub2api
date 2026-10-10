//go:build unit

package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAntigravityGatewayService_Forward_OptionalSessionRateLimitCleanup(t *testing.T) {
	for _, statusCode := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		for _, tc := range []struct {
			name       string
			options    []ForwardClaudeOption
			sticky     bool
			wantDelete bool
		}{
			{name: "no_option", sticky: true},
			{name: "nil_option", options: []ForwardClaudeOption{nil}, sticky: true},
			{name: "empty_session", options: []ForwardClaudeOption{WithForwardClaudeSession(77, "")}, sticky: true},
			{name: "bound_session", options: []ForwardClaudeOption{WithForwardClaudeSession(77, "gemini:sticky-runtime")}, sticky: true, wantDelete: true},
			{name: "new_binding", options: []ForwardClaudeOption{WithForwardClaudeSession(77, "gemini:sticky-runtime")}, wantDelete: true},
		} {
			t.Run(fmt.Sprintf("%d/%s", statusCode, tc.name), func(t *testing.T) {
				body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
				responseBody := []byte(`{"error":{"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","metadata":{"model":"gemini-2.5-pro"},"reason":"RATE_LIMIT_EXCEEDED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"15s"}]}}`)
				upstream := &queuedHTTPUpstreamStub{responses: []*http.Response{{
					StatusCode: statusCode,
					Header:     http.Header{},
					Body:       io.NopCloser(bytes.NewReader(responseBody)),
				}}}
				repo := &stubAntigravityAccountRepo{}
				cache := &stubSmartRetryCache{}
				svc := &AntigravityGatewayService{
					tokenProvider: &AntigravityTokenProvider{},
					httpUpstream:  upstream,
					accountRepo:   repo,
					cache:         cache,
				}
				account := &Account{
					ID:          44,
					Name:        "acc-claude-session-rate-limited",
					Platform:    PlatformAntigravity,
					Type:        AccountTypeOAuth,
					Status:      StatusActive,
					Schedulable: true,
					Concurrency: 1,
					Credentials: map[string]any{
						"access_token": "token",
						"expires_at":   time.Now().Add(time.Hour).Format(time.RFC3339),
						"project_id":   "proj",
						"model_mapping": map[string]any{
							"claude-sonnet-4-5": "gemini-2.5-pro",
						},
					},
					Extra: map[string]any{"mixed_scheduling": true},
				}

				result, err := svc.Forward(context.Background(), c, account, body, tc.sticky, tc.options...)

				require.Nil(t, result)
				var failoverErr *UpstreamFailoverError
				require.ErrorAs(t, err, &failoverErr)
				require.Equal(t, http.StatusServiceUnavailable, failoverErr.StatusCode)
				require.Equal(t, tc.sticky, failoverErr.ForceCacheBilling)
				require.Equal(t, 1, upstream.callCount)
				require.Len(t, repo.modelRateLimitCalls, 2)
				require.Equal(t, "gemini-2.5-pro", repo.modelRateLimitCalls[0].modelKey)
				require.Equal(t, antigravityGeminiModelRateLimitKey, repo.modelRateLimitCalls[1].modelKey)
				if tc.wantDelete {
					require.Len(t, cache.deleteCalls, 1)
					require.Equal(t, int64(77), cache.deleteCalls[0].groupID)
					require.Equal(t, "gemini:sticky-runtime", cache.deleteCalls[0].sessionHash)
				} else {
					require.Empty(t, cache.deleteCalls)
				}
			})
		}
	}
}
