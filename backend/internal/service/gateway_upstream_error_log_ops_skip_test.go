//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 上游切号/不可重试错误已由 appendOpsUpstreamError 结构化记录，
// 对应的文本日志照常输出，但不得再写入运维系统日志库。
func TestGatewayUpstreamErrorLogsSkipOpsSystemLogWhenRecordedAsOpsUpstreamError(t *testing.T) {
	for _, tc := range []struct {
		name        string
		passthrough bool
		status      int
		logMessage  string
		wantKind    string
	}{
		{name: "managed_failover", status: http.StatusTooManyRequests, logMessage: "[Forward] Upstream error (failover)", wantKind: "failover"},
		{name: "passthrough_failover", passthrough: true, status: http.StatusTooManyRequests, logMessage: "[Anthropic Passthrough] Upstream error (failover)", wantKind: "failover"},
		{name: "managed_non_retryable", status: http.StatusBadRequest, logMessage: "[Forward] Upstream error (non-retryable)", wantKind: "http_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			captured, restore := captureStructuredLog(t)
			defer restore()

			upstream := &fallbackSequenceUpstream{responses: []*http.Response{{
				StatusCode: tc.status,
				Header:     http.Header{"Content-Type": {"application/json"}, "X-Request-Id": {"rid-ops-skip"}},
				Body:       io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)),
			}}}
			svc := fallbackNativeGatewayForTest(t, upstream, nil)
			account := fallbackNativeAccountForTest(tc.passthrough)
			// 池模式下限流不落库账号状态，测试无需账号仓储。
			account.Credentials["pool_mode"] = true
			parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-5","max_tokens":1024,"messages":[{"role":"user","content":"hello"}]}`)), PlatformAnthropic)
			require.NoError(t, err)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

			_, err = svc.Forward(context.Background(), c, account, parsed)
			require.Error(t, err)

			raw, ok := c.Get(OpsUpstreamErrorsKey)
			require.True(t, ok, "the same branch must record the event via appendOpsUpstreamError")
			events, ok := raw.([]*OpsUpstreamErrorEvent)
			require.True(t, ok)
			require.NotEmpty(t, events)
			last := events[len(events)-1]
			require.Equal(t, tc.wantKind, last.Kind)
			require.Equal(t, tc.status, last.UpstreamStatusCode)
			require.Equal(t, account.ID, last.AccountID)

			var logEvent *logger.LogEvent
			captured.mu.Lock()
			for _, ev := range captured.events {
				if strings.Contains(ev.Message, tc.logMessage) {
					logEvent = ev
					break
				}
			}
			captured.mu.Unlock()
			require.NotNil(t, logEvent, "the text log must still be emitted")
			require.Equal(t, "error", logEvent.Level)
			require.Contains(t, logEvent.Message, fmt.Sprintf("Status=%d RequestID=rid-ops-skip", tc.status))
			require.Equal(t, true, logEvent.Fields[logger.OpsSystemLogSkipField])
			require.False(t, (&OpsSystemLogSink{}).shouldIndex(logEvent), "ops system log sink must not index a duplicated upstream error")
		})
	}
}
