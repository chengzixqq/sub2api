package service

import (
	"context"
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

func TestGatewayHandleErrorResponse_PreservesPayloadTooLarge(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, body := range []string{
		`<html><title>413 Request Entity Too Large</title><body>nginx at private.example</body></html>`,
		`{"error":{"message":"request too large at https://private.example/v1?key=hidden"}}`,
		``,
	} {
		t.Run(fmt.Sprintf("body_%d_bytes", len(body)), func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Set(redactUpstreamURLContextKey, true)
			resp := &http.Response{StatusCode: http.StatusRequestEntityTooLarge, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
			account := &Account{ID: 66, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
			_, err := (&GatewayService{}).handleErrorResponse(context.Background(), resp, c, account)
			require.Error(t, err)
			var failoverErr *UpstreamFailoverError
			require.False(t, errors.As(err, &failoverErr))
			require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
			require.Equal(t, "request_too_large", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
			require.Contains(t, gjson.GetBytes(rec.Body.Bytes(), "error.message").String(), "too large")
			require.NotContains(t, rec.Body.String(), "private.example")
			require.NotContains(t, rec.Body.String(), "hidden")
			require.True(t, IsResponseCommitted(c))
			require.Equal(t, 413, c.GetInt(OpsUpstreamStatusCodeKey))
		})
	}
}

func TestGatewayService_PayloadTooLargeIsNotRetryable(t *testing.T) {
	for _, custom := range []bool{false, true} {
		account := newAnthropicAPIKeyAccountForTest()
		account.Credentials["custom_error_codes_enabled"] = custom
		account.Credentials["custom_error_codes"] = []any{float64(401), float64(429)}
		require.False(t, (&GatewayService{}).shouldRetryUpstreamError(account, http.StatusRequestEntityTooLarge))
	}
}

type payloadTooLargeUpstream struct {
	calls int
}

func (u *payloadTooLargeUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	return &http.Response{
		StatusCode: http.StatusRequestEntityTooLarge,
		Header:     http.Header{"Content-Type": {"text/html"}},
		Body:       io.NopCloser(strings.NewReader("<html><title>413 Request Entity Too Large</title><body>nginx</body></html>")),
	}, nil
}

func (u *payloadTooLargeUpstream) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, concurrency)
}

func TestGatewayService_ForwardPayloadTooLargeDoesNotRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, passthrough := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, custom := range []bool{false, true} {
				t.Run(fmt.Sprintf("passthrough_%t_stream_%t_custom_%t", passthrough, stream, custom), func(t *testing.T) {
					account := newAnthropicAPIKeyAccountForTest()
					account.Extra["anthropic_passthrough"] = passthrough
					account.Credentials["pool_mode"] = true
					account.Credentials["pool_mode_retry_status_codes"] = []any{float64(413), float64(502)}
					account.Credentials["custom_error_codes_enabled"] = custom
					account.Credentials["custom_error_codes"] = []any{float64(401), float64(429)}
					upstream := &payloadTooLargeUpstream{}
					svc := &GatewayService{
						cfg:              &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
						httpUpstream:     upstream,
						deferredService:  &DeferredService{},
						rateLimitService: &RateLimitService{},
					}
					body := []byte(fmt.Sprintf(`{"model":"claude-fable-5","stream":%t,"max_tokens":200000,"messages":[{"role":"user","content":"hello"}]}`, stream))
					rec := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(rec)
					c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body)))
					result, err := svc.Forward(context.Background(), c, account, &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-fable-5", Stream: stream})
					require.Error(t, err)
					require.Nil(t, result)
					var failoverErr *UpstreamFailoverError
					require.False(t, errors.As(err, &failoverErr))
					require.Equal(t, 1, upstream.calls)
					require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
					require.Equal(t, "request_too_large", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
				})
			}
		}
	}
}
