//go:build unit

package handler

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type claudeStreamFaultUpstream struct {
	service.HTTPUpstream
	body io.Reader
}

func (u claudeStreamFaultUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(u.body)}, nil
}

func TestAntigravityTerminalOpsRecord(t *testing.T) {
	const request = `{"model":"claude-sonnet-4-5","stream":true,"max_tokens":32,"messages":[{"role":"user","content":"hi"}]}`
	const chunk = "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]}}]}}\n\n"
	for _, tc := range []struct {
		reason, category string
		previous         bool
	}{
		{"stream_read_error", "transport_or_stream", false}, {"stream_read_error", "transport_or_stream", true},
		{"response_too_large", "upstream_5xx", false}, {"stream_timeout", "timeout", false},
		{"missing_provider", "upstream_5xx", false},
	} {
		name := tc.reason
		if tc.previous {
			name += "/after_retry"
		}
		t.Run(name, func(t *testing.T) {
			withOpsFilterSettings(t, func(t *testing.T, ops *service.OpsService) {
				setupOpsErrorLogTestQueue(t, 2)

				r := gin.New()
				r.Use(OpsErrorLoggerMiddleware(ops))
				r.POST("/v1/messages", func(c *gin.Context) {
					cfg := &config.Config{}
					cfg.Gateway.MaxLineSize = 65536
					var fault io.Reader = iotest.ErrReader(errors.New("test stream failure"))
					if tc.reason == "response_too_large" {
						fault = strings.NewReader(strings.Repeat("a", 131072))
					}
					if tc.reason == "stream_timeout" {
						pr, pw := io.Pipe()
						defer pr.Close()
						defer pw.Close()
						cfg.Gateway.StreamDataIntervalTimeout = 1
						fault = pr
					}
					provider := service.NewAntigravityTokenProvider(nil, nil, nil)
					if tc.reason == "missing_provider" {
						provider = nil
						c.Header("Content-Type", "text/event-stream")
						_, err := c.Writer.WriteString("data: {\"type\": \"ping\"}\n\n")
						require.NoError(t, err)
						c.Writer.Flush()
					}
					if tc.previous {
						c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{AccountID: 16, UpstreamStatusCode: 502, Detail: "earlier upstream failure", Message: "earlier upstream failure", Kind: "http_error"}})
					}
					gateway := service.NewAntigravityGatewayService(nil, nil, nil, provider, nil, claudeStreamFaultUpstream{body: io.MultiReader(strings.NewReader(chunk), fault)}, service.NewSettingService(&ingressRejectSettingRepo{}, cfg), nil)
					account := &service.Account{ID: 23, Platform: service.PlatformAntigravity, Type: service.AccountTypeOAuth, Concurrency: 1, Credentials: map[string]any{"project_id": "test-project", "access_token": "test-token", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339), "model_mapping": map[string]any{"claude-sonnet-4-5": "claude-sonnet-4-5"}}}
					_, err := gateway.Forward(c.Request.Context(), c, account, []byte(request), false)
					require.Error(t, err)
					(&GatewayHandler{}).ensureForwardErrorResponse(c, true)
				})
				rec := httptest.NewRecorder()
				r.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(request)))
				t.Logf("wire=%q rows=%d", rec.Body.String(), len(opsErrorLogQueue))
				if tc.reason == "missing_provider" {
					require.Contains(t, rec.Body.String(), "event: error\ndata:")
					require.Empty(t, opsErrorLogQueue)
					logMonitorRow(t, nil)
					return
				}
				require.Len(t, opsErrorLogQueue, 1)
				e := (<-opsErrorLogQueue).entry
				aggregate, detailCategory := logMonitorRow(t, e)
				require.Equal(t, tc.category, aggregate)
				require.Equal(t, "upstream_5xx", detailCategory)
				detail := ""
				if e.UpstreamErrorDetail != nil {
					detail = *e.UpstreamErrorDetail
				}
				status := 0
				if e.UpstreamStatusCode != nil {
					status = *e.UpstreamStatusCode
				}
				cat := service.ClassifyChannelMonitorV2Error(service.ChannelMonitorV2ErrorInput{StatusCode: e.StatusCode, UpstreamStatusCode: status, ErrorType: e.ErrorType, ErrorOwner: e.ErrorOwner, Message: strings.Join([]string{e.ErrorMessage, e.ErrorBody, detail}, " ")})
				t.Logf("ops=%s/%d/%s/%s/%s category=%s detail=%q", e.ErrorType, e.StatusCode, e.ErrorPhase, e.Severity, e.ErrorOwner, cat, detail)
				require.Equal(t, 502, e.StatusCode)
				require.Equal(t, "P1", e.Severity)
				require.Equal(t, tc.category, cat)
				require.Equal(t, 1, strings.Count(rec.Body.String(), "event: error\n"))
				require.NotContains(t, rec.Body.String(), "\n\ndata: {\"type\":\"error\"")
				if tc.reason != "missing_provider" {
					require.Equal(t, []string{"upstream_error", "upstream", "provider"}, []string{e.ErrorType, e.ErrorPhase, e.ErrorOwner})
					if tc.previous {
						require.NotNil(t, e.UpstreamErrorsJSON)
						events, err := service.ParseOpsUpstreamErrors(*e.UpstreamErrorsJSON)
						require.NoError(t, err)
						require.Len(t, events, 1)
						require.Equal(t, "earlier upstream failure", events[0].Detail)
						require.Equal(t, "earlier upstream failure", detail)
					} else {
						require.Equal(t, tc.reason, detail)
					}
				}
			})
		})
	}
}
