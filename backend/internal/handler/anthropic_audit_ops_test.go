//go:build unit

package handler

import (
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestAnthropicAuditOpsPersistence(t *testing.T) {
	const legacy = "内容审计命中风险规则，请调整输入后重试"
	for _, tc := range []struct {
		name, msg, wire string
		modern          bool
		status          int
	}{
		{"default", legacy, "Request blocked by content policy. Revise your input and try again.", false, 403},
		{"hash", legacy + "（hash: abc）", "Request blocked by content policy. Revise your input and try again.", false, 403},
		{"custom", "管理员配置的消息", "管理员配置的消息", false, 403},
		{"prompt", "提示词安全审计拒绝了该请求，请调整输入后重试", "Request blocked by prompt security policy. Revise your input and try again.", true, 403},
		{"unavailable", "提示词安全审计暂时不可用，请稍后重试", "Prompt security checks are temporarily unavailable. Please retry later.", true, 503},
	} {
		for _, bridge := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/bridge=%t", tc.name, bridge), func(t *testing.T) {
				withOpsFilterSettings(t, func(t *testing.T, ops *service.OpsService) {
					setupOpsErrorLogTestQueue(t, 2)
					r := gin.New()
					r.Use(OpsErrorLoggerMiddleware(ops))
					r.POST("/v1/messages", func(c *gin.Context) {
						d := &securityaudit.Decision{Kind: securityaudit.DecisionBlock, HTTPStatus: tc.status, ClientMessage: tc.msg}
						if tc.modern {
							d.ErrorCode = "prompt_guard_rejected"
							if tc.status == 503 {
								d.Kind = securityaudit.DecisionKind("unavailable")
								d.ErrorCode = "prompt_guard_unavailable"
							}
						} else {
							d.Legacy = &securityaudit.LegacyDecision{Blocked: true, Message: tc.msg}
						}
						if bridge {
							(&OpenAIGatewayHandler{}).anthropicSecurityAuditError(c, d)
						} else {
							(&GatewayHandler{}).anthropicSecurityAuditError(c, d)
						}
					})
					w := httptest.NewRecorder()
					r.ServeHTTP(w, httptest.NewRequest("POST", "/v1/messages", nil))
					require.Len(t, opsErrorLogQueue, 1)
					row := (<-opsErrorLogQueue).entry
					aggregate, detail := logMonitorRow(t, row)
					wantAgg, wantDetail := "content_policy", "upstream_forbidden"
					if tc.modern {
						wantAgg = "upstream_forbidden"
					}
					if tc.status == 503 {
						wantAgg, wantDetail = "internal", "internal"
					}
					require.Equal(t, wantAgg, aggregate)
					require.Equal(t, wantDetail, detail)
					require.Equal(t, tc.msg, row.ErrorMessage)
					require.Equal(t, tc.wire, parseOpsErrorResponse(w.Body.Bytes()).Message)
					if !tc.modern {
						require.Contains(t, row.ErrorBody, "content_policy_violation")
					}
				})
			})
		}
	}
}
