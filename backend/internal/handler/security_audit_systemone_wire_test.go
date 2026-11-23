//go:build unit

package handler

import (
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/model"
	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSystemOneSecurityAuditWire(t *testing.T) {
	const blocked = "提示词安全审计拒绝了该请求，请调整输入后重试"
	const unavailable = "提示词安全审计暂时不可用，请稍后重试"
	const legacy = "内容审计命中风险规则，请调整输入后重试"
	for _, tc := range []struct {
		name     string
		decision securityaudit.Decision
		want     string
	}{
		{"block", securityaudit.Decision{Kind: securityaudit.DecisionBlock, HTTPStatus: 403, ErrorCode: "prompt_block", ClientMessage: blocked}, `{"error":{"code":"prompt_block","message":"` + blocked + `","type":"permission_error"},"type":"error"}`},
		{"unavailable", securityaudit.Decision{Kind: securityaudit.DecisionUnavailable, HTTPStatus: 503, ErrorCode: "prompt_unavailable", ClientMessage: unavailable}, `{"error":{"code":"prompt_unavailable","message":"` + unavailable + `","type":"api_error"},"type":"error"}`},
		{"legacy", securityaudit.Decision{HTTPStatus: 403, Legacy: &securityaudit.LegacyDecision{Blocked: true, Message: legacy}}, `{"error":{"message":"` + legacy + `","type":"content_policy_violation"},"type":"error"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/systemone", nil)
			(&GatewayHandler{}).anthropicSecurityAuditError(c, &tc.decision)
			t.Logf("WIRE %s %d %q %q", t.Name(), rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
			require.Equal(t, tc.decision.HTTPStatus, rec.Code)
			require.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
			require.Equal(t, tc.want, rec.Body.String())
		})
	}
}

func TestSystemOneFailoverRuleWire(t *testing.T) {
	const custom = "retry at https://relay.example/path?key=private"
	rule := newBodyLimitPassthroughRule(418, custom)
	rule.Platforms = []string{service.PlatformTypeSafe}
	rule.ErrorCodes = []int{502}
	h := &GatewayHandler{errorPassthroughService: service.NewErrorPassthroughService(&errorPassthroughRepoStub{rules: []*model.ErrorPassthroughRule{rule}}, nil)}
	for _, redact := range []bool{true, false} {
		t.Run(fmt.Sprint(redact), func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/v1/systemone", nil)
			c.Set("claude_customization_redact_upstream_url", redact)
			h.handleFailoverExhausted(c, &service.UpstreamFailoverError{StatusCode: 502, ResponseBody: []byte(`{"error":{"message":"upstream failed"}}`)}, service.PlatformTypeSafe, false)
			msg := custom
			if redact {
				msg = "retry at https://***.***/path"
			}
			t.Logf("WIRE %s %d %q %q", t.Name(), rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
			require.Equal(t, 418, rec.Code)
			require.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
			require.Equal(t, `{"error":{"message":"`+msg+`","type":"upstream_error"},"type":"error"}`, rec.Body.String())
		})
	}
}
