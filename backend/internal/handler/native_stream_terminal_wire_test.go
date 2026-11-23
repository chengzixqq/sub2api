//go:build unit

package handler

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"testing/iotest"

	"golang.org/x/net/http2"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/model"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type nativeWireUpstream struct {
	service.HTTPUpstream
	body io.ReadCloser
}

func (u *nativeWireUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	resp := preContentSSEResponse(u.body)
	resp.Header.Set("Request-Id", "req_011C123456789ABCDEFGHJKL")
	return resp, nil
}
func (u *nativeWireUpstream) DoWithTLS(r *http.Request, p string, id int64, n int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(r, p, id, n)
}

func forwardNativeWireStream(t *testing.T, c *gin.Context, cfg *config.Config, reader io.ReadCloser, overrides map[string]any) error {
	t.Helper()
	settings := service.NewSettingService(&ingressRejectSettingRepo{}, cfg)
	gw := service.NewGatewayService(nil, nil, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil, &nativeWireUpstream{body: reader}, &service.DeferredService{}, nil, nil, nil, nil, settings, nil, nil, nil, nil, nil, nil, nil)
	account := &service.Account{ID: 11, Type: service.AccountTypeAPIKey, Platform: service.PlatformAnthropic, Credentials: map[string]any{"api_key": "upstream-test"}, Extra: map[string]any{service.AccountClaudeCustomizationExtraKey: overrides}}
	parsed, err := service.ParseGatewayRequest(service.NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}],"max_tokens":16,"stream":true}`)), service.PlatformAnthropic)
	require.NoError(t, err)
	_, err = gw.Forward(c.Request.Context(), c, account, parsed)
	return err
}

type timeoutNetError struct{}

func (timeoutNetError) Error() string   { return "i/o timeout" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return true }

func TestNativeStreamTerminalErrorWire(t *testing.T) {
	const start = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":5}}}\n\n"
	for _, tc := range []struct {
		name, reason, msg, wireType, category string
		err                                   error
	}{
		{"unexpected_EOF", "stream_read_error", "upstream stream disconnected: unexpected EOF", "api_error", "transport_or_stream", io.ErrUnexpectedEOF},
		{"reset", "stream_read_error", "upstream stream disconnected: connection reset by peer", "api_error", "transport_or_stream", syscall.ECONNRESET},
		{"refused", "stream_read_error", "upstream stream disconnected: connection refused", "api_error", "transport_or_stream", syscall.ECONNREFUSED},
		{"pipe", "stream_read_error", "upstream stream disconnected: broken pipe", "api_error", "transport_or_stream", syscall.EPIPE},
		{"aborted", "stream_read_error", "upstream stream disconnected: connection aborted", "api_error", "transport_or_stream", syscall.ECONNABORTED},
		{"read_timeout", "stream_read_error", "upstream stream disconnected: read timeout", "api_error", "timeout", &net.OpError{Op: "read", Err: timeoutNetError{}}},
		{"goaway", "stream_read_error", "upstream stream disconnected: upstream connection error", "api_error", "transport_or_stream", http2.GoAwayError{}},
		{"h2_stream", "stream_read_error", "upstream stream disconnected: upstream connection error", "api_error", "transport_or_stream", http2.StreamError{}},
		{"generic", "stream_read_error", "upstream stream disconnected: upstream connection error", "api_error", "transport_or_stream", errors.New("upstream failure")},
		{"EOF", "stream_read_error", "upstream stream disconnected: EOF", "api_error", "transport_or_stream", fmt.Errorf("read failed: %w", io.EOF)},
		{"timed_out", "stream_read_error", "upstream stream disconnected: connection timed out", "api_error", "transport_or_stream", syscall.ETIMEDOUT},
		{"network", "stream_read_error", "upstream stream disconnected: read network error", "api_error", "transport_or_stream", &net.OpError{Op: "read", Err: errors.New("wire")}},
		{"line_limit", "response_too_large", "upstream SSE line exceeded 65536 bytes", "api_error", "upstream_5xx", nil},
		{"timeout", "stream_timeout", "upstream stream idle for 1s", "timeout_error", "timeout", nil},
	} {
		for _, fill := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fill=%t", tc.name, fill), func(t *testing.T) {
				withOpsFilterSettings(t, func(t *testing.T, ops *service.OpsService) {
					setupOpsErrorLogTestQueue(t, 2)
					r := gin.New()

					r.Use(OpsErrorLoggerMiddleware(ops))
					appended := false
					r.POST("/v1/messages", func(c *gin.Context) {
						cfg := &config.Config{}
						var reader io.ReadCloser
						switch tc.reason {
						case "response_too_large":
							cfg.Gateway.MaxLineSize = 65536
							reader = io.NopCloser(strings.NewReader(start + strings.Repeat("x", 131072)))
						case "stream_timeout":
							cfg.Gateway.StreamDataIntervalTimeout = 1
							pr, pw := io.Pipe()
							defer pw.Close()
							reader = pr
						default:
							reader = io.NopCloser(io.MultiReader(strings.NewReader(start), iotest.ErrReader(tc.err)))
						}
						overrides := map[string]any{}
						if fill {
							overrides["request_id_mode"] = "fill"
						}
						require.Error(t, forwardNativeWireStream(t, c, cfg, reader, overrides))
						appended = (&GatewayHandler{}).ensureForwardErrorResponse(c, c.Writer.Written())
					})
					rec := httptest.NewRecorder()
					r.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", nil))
					require.Len(t, opsErrorLogQueue, 1, rec.Body.String())
					row := (<-opsErrorLogQueue).entry
					aggregate, detailCategory := logMonitorRow(t, row)
					require.Equal(t, tc.category, aggregate)
					require.Equal(t, "upstream_5xx", detailCategory)
					require.Equal(t, "Upstream request failed", row.ErrorMessage)
					require.NotNil(t, row.UpstreamErrorMessage)
					require.Equal(t, "Upstream request failed", *row.UpstreamErrorMessage)
					msg := row.ErrorMessage + " " + row.ErrorBody
					var detail string
					if row.UpstreamErrorMessage != nil {
						msg += " " + *row.UpstreamErrorMessage
					}
					if row.UpstreamErrorDetail != nil {
						detail = *row.UpstreamErrorDetail
						msg += " " + detail
					}
					status := 0
					if row.UpstreamStatusCode != nil {
						status = *row.UpstreamStatusCode
					}
					cat := service.ClassifyChannelMonitorV2Error(service.ChannelMonitorV2ErrorInput{ErrorType: row.ErrorType, ErrorOwner: row.ErrorOwner, ErrorSource: row.ErrorSource, StatusCode: row.StatusCode, UpstreamStatusCode: status, Message: msg})
					t.Logf("OPS %s type=%s status=%d phase=%s owner=%s severity=%s category=%s", t.Name(), row.ErrorType, row.StatusCode, row.ErrorPhase, row.ErrorOwner, row.Severity, cat)
					t.Logf("terminal fallback=%t detail=%q body=%q", appended, detail, rec.Body.String())
					require.Equal(t, []string{"upstream_error", "upstream", "provider", "P1", tc.category}, []string{row.ErrorType, row.ErrorPhase, row.ErrorOwner, row.Severity, cat})
					require.Equal(t, 502, row.StatusCode)
					require.Equal(t, tc.reason, detail)
					require.False(t, appended)
					wire := `{"type":"error","error":{"type":"` + tc.wireType + `","message":"` + tc.msg + `"}`
					if fill {
						wire += `,"request_id":"req_011C123456789ABCDEFGHJKL"`
					}
					require.True(t, strings.HasSuffix(rec.Body.String(), "event: error\ndata: "+wire+"}\n\n"), rec.Body.String())
					require.Equal(t, 1, strings.Count(rec.Body.String(), `"type":"error"`))
				})
			})
		}
	}
}

func TestNativeFailoverRuleURLRedaction(t *testing.T) {
	const custom = "retry at https://relay.example/private?key=private#fragment"
	for _, redact := range []bool{true, false} {
		t.Run(fmt.Sprint(redact), func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
			err := forwardNativeWireStream(t, c, &config.Config{}, io.NopCloser(iotest.ErrReader(io.ErrUnexpectedEOF)), map[string]any{"url_redaction_enabled": redact})
			var fail *service.UpstreamFailoverError
			require.ErrorAs(t, err, &fail)
			rule := newBodyLimitPassthroughRule(418, custom)
			rule.Platforms = []string{model.PlatformAnthropic}
			rule.ErrorCodes = []int{502}
			rules := service.NewErrorPassthroughService(&errorPassthroughRepoStub{rules: []*model.ErrorPassthroughRule{rule}}, nil)
			(&GatewayHandler{errorPassthroughService: rules}).handleFailoverExhausted(c, fail, service.PlatformAnthropic, false)
			msg := gjson.Get(rec.Body.String(), "error.message").String()
			t.Logf("REDACT %s %d %q", t.Name(), rec.Code, msg)
			require.Equal(t, 418, rec.Code)
			want := custom
			if redact {
				want = "retry at https://***.***/private"
			}
			require.Equal(t, want, msg)
		})
	}
}
