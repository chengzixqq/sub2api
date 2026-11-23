//go:build unit

package handler

import (
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestLocalClaudeOpsClassification(t *testing.T) {
	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	for _, tc := range []struct {
		status                                 int
		original, want, phase, severity, owner string
	}{
		{402, "billing_error", "billing_error", "request", "P3", "client"}, {503, "probe_unavailable", "api_error", "internal", "P1", "platform"},
		{404, "model_not_found", "model_not_found", "request", "P3", "client"}, {429, "rate_limit_error", "rate_limit_error", "upstream", "P1", "provider"},
		{503, "api_error", "api_error", "internal", "P1", "platform"}, {403, "content_policy_violation", "api_error", "internal", "P2", "platform"}, {403, "permission_error", "permission_error", "request", "P3", "client"},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/%s/%t", tc.status, tc.original, stream), func(t *testing.T) {
				setupOpsErrorLogTestQueue(t, 2)
				r := gin.New()
				r.Use(OpsErrorLoggerMiddleware(ops))
				r.POST("/v1/messages", func(c *gin.Context) {
					if stream {
						c.Header("Content-Type", "text/event-stream")
						_, err := c.Writer.WriteString(": ping\n\n")
						require.NoError(t, err)
						c.Writer.Flush()
					}
					h := &GatewayHandler{}
					if tc.status == 403 && !stream {
						h.errorResponse(c, tc.status, tc.original, "local failure")
					} else {
						h.handleLocalStreamingAwareError(c, tc.status, tc.original, "local failure", stream)
					}
				})
				rec := httptest.NewRecorder()
				r.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", nil))
				require.Len(t, opsErrorLogQueue, 1)
				e := (<-opsErrorLogQueue).entry
				t.Logf("ops %s/%s/%s/%s", e.ErrorType, e.ErrorPhase, e.Severity, e.ErrorOwner)
				severity := tc.severity
				if stream && tc.original == "content_policy_violation" {
					severity = "P1"
				}
				require.Equal(t, []string{tc.want, tc.phase, severity, tc.owner}, []string{e.ErrorType, e.ErrorPhase, e.Severity, e.ErrorOwner})
			})
		}
	}
}
