//go:build unit

package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestLocalBillingOpsProductionBaseline(t *testing.T) {
	for _, billErr := range []error{service.ErrSubscriptionInvalid, service.ErrUserRPMExceeded, service.ErrInsufficientBalance, service.ErrAPIKeyRateLimit5hExceeded, service.ErrAPIKeyRateLimit1dExceeded, service.ErrAPIKeyRateLimit7dExceeded, service.ErrAPIKeyQuotaExhausted, service.ErrAPIKeyExpired, service.ErrBillingServiceUnavailable} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", billErr, stream), func(t *testing.T) {
				setupOpsErrorLogTestQueue(t, 2)
				ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
				r := gin.New()
				r.Use(OpsErrorLoggerMiddleware(ops))
				r.POST("/v1/messages", func(c *gin.Context) {
					status, code, msg, _ := billingErrorDetailsForRequest(c, billErr)
					if stream {
						c.Header("Content-Type", "text/event-stream")
						_, err := c.Writer.WriteString(": ping\n\n")
						require.NoError(t, err)
						c.Writer.Flush()
					}
					(&GatewayHandler{}).handleStreamingAwareError(c, status, code, msg, stream)
				})
				rec := httptest.NewRecorder()
				r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
				status, _, _, _ := billingErrorDetails(billErr)
				wire := map[int]string{403: "permission_error", 429: "rate_limit_error", 503: "api_error"}[status]
				require.Contains(t, rec.Body.String(), `"type":"`+wire+`"`)
				require.Len(t, opsErrorLogQueue, 1)
				entry := (<-opsErrorLogQueue).entry
				t.Logf("ops=%s/%s/%s", entry.ErrorType, entry.ErrorPhase, entry.Severity)
				want := map[int][3]string{403: {"billing_error", "request", "P3"}, 429: {"api_error", "request", "P1"}, 503: {"api_error", "internal", "P1"}}[status]
				require.Equal(t, want, [3]string{entry.ErrorType, entry.ErrorPhase, entry.Severity})
			})
		}
	}
}

func TestOpenAIFailedStreamWithCompactionUsage(t *testing.T) {
	u := service.OpenAIUsage{IterationUsage: &service.ClaudeIterationUsage{Iterations: []service.ClaudeUsageIteration{{Type: "compaction", Usage: service.UsageTokens{InputTokens: 50}}}}}
	require.True(t, openAIForwardResultHasActualBillableUsage(&service.OpenAIForwardResult{Usage: u}))
}
