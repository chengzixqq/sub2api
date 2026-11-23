package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/googleapi"
	"github.com/Wei-Shaw/sub2api/internal/securityaudit"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
)

func (h *OpenAIGatewayHandler) openAISecurityAuditError(c *gin.Context, decision *securityaudit.Decision) {
	if decision == nil {
		return
	}
	if decision.Legacy != nil && decision.Legacy.Blocked {
		h.errorResponse(c, securityAuditStatus(decision), securityAuditErrorCode(decision), securityAuditMessage(decision))
		return
	}
	errType := "api_error"
	if decision.Kind == securityaudit.DecisionBlock {
		errType = "permission_error"
	}
	c.JSON(securityAuditStatus(decision), gin.H{"error": gin.H{
		"type": errType, "code": securityAuditErrorCode(decision), "message": securityAuditMessage(decision),
	}})
}

func (h *GatewayHandler) openAISecurityAuditError(c *gin.Context, decision *securityaudit.Decision) {
	if decision == nil {
		return
	}
	if decision.Legacy != nil && decision.Legacy.Blocked {
		h.chatCompletionsErrorResponse(c, securityAuditStatus(decision), securityAuditErrorCode(decision), securityAuditMessage(decision))
		return
	}
	errType := "api_error"
	if decision.Kind == securityaudit.DecisionBlock {
		errType = "permission_error"
	}
	c.JSON(securityAuditStatus(decision), gin.H{"error": gin.H{
		"type": errType, "code": securityAuditErrorCode(decision), "message": securityAuditMessage(decision),
	}})
}

func (h *GatewayHandler) responsesSecurityAuditError(c *gin.Context, decision *securityaudit.Decision) {
	if decision == nil {
		return
	}
	if decision.Legacy != nil && decision.Legacy.Blocked {
		h.responsesErrorResponse(c, securityAuditStatus(decision), securityAuditErrorCode(decision), securityAuditMessage(decision))
		return
	}
	c.JSON(securityAuditStatus(decision), gin.H{"error": gin.H{
		"type": "api_error", "code": securityAuditErrorCode(decision), "message": securityAuditMessage(decision),
	}})
}

// Translate the built-in audit messages at the Messages protocol boundary.
func anthropicSecurityAuditMessage(c *gin.Context, decision *securityaudit.Decision) string {
	message := securityAuditMessage(decision)
	c.Set(service.OpsAnthropicErrorMessageKey, message)
	const moderationBlock = "内容审计命中风险规则，请调整输入后重试"
	if strings.HasPrefix(message, moderationBlock+"（hash: ") && strings.HasSuffix(message, "）") {
		return "Request blocked by content policy. Revise your input and try again."
	}
	switch message {
	case "提示词安全审计拒绝了该请求，请调整输入后重试":
		return "Request blocked by prompt security policy. Revise your input and try again."
	case "提示词安全审计暂时不可用，请稍后重试":
		return "Prompt security checks are temporarily unavailable. Please retry later."
	case moderationBlock:
		return "Request blocked by content policy. Revise your input and try again."
	default:
		return message
	}
}

func (h *GatewayHandler) anthropicSecurityAuditError(c *gin.Context, decision *securityaudit.Decision) {
	if decision == nil {
		return
	}
	message := securityAuditMessage(decision)
	if middleware2.UsesAnthropicGatewayErrors(c) {
		message = anthropicSecurityAuditMessage(c, decision)
	}
	if decision.Legacy != nil && decision.Legacy.Blocked {
		h.errorResponse(c, securityAuditStatus(decision), securityAuditErrorCode(decision), message)
		return
	}
	errType := "api_error"
	if decision.Kind == securityaudit.DecisionBlock {
		errType = "permission_error"
	}
	h.errorResponseWithCode(c, securityAuditStatus(decision), errType, securityAuditErrorCode(decision), message)
}

func (h *OpenAIGatewayHandler) anthropicSecurityAuditError(c *gin.Context, decision *securityaudit.Decision) {
	if decision == nil {
		return
	}
	if decision.Legacy != nil && decision.Legacy.Blocked {
		h.anthropicErrorResponse(c, securityAuditStatus(decision), securityAuditErrorCode(decision), anthropicSecurityAuditMessage(c, decision))
		return
	}
	errType := "api_error"
	if decision.Kind == securityaudit.DecisionBlock {
		errType = "permission_error"
	}
	status, code := securityAuditStatus(decision), securityAuditErrorCode(decision)
	message := anthropicSecurityAuditMessage(c, decision)
	if service.IsAnthropicMessagesRequest(c) {
		service.PreserveOpsErrorResponse(c, service.AnthropicErrorWithCodeAndRequestID(errType, code, securityAuditMessage(decision), ""), "")
		service.WriteAnthropicError(c, status, errType, service.MessagesErrorType(c, status, errType), code, message, service.FilledAnthropicRequestID(c), false)
		return
	}
	h.handleStreamingAwareErrorWithCode(c, status, errType, code, message, false, false)
}

func googleSecurityAuditError(c *gin.Context, decision *securityaudit.Decision) {
	if decision == nil {
		return
	}
	if decision.Legacy != nil && decision.Legacy.Blocked {
		googleErrorWithType(c, securityAuditStatus(decision), securityAuditErrorCode(decision), "", securityAuditMessage(decision))
		return
	}
	errType := "api_error"
	if decision.Kind == securityaudit.DecisionBlock {
		errType = "permission_error"
	}
	service.SetOpsLocalErrorType(c, errType, securityAuditErrorCode(decision))
	status := securityAuditStatus(decision)
	googleStatus := googleapi.HTTPStatusToGoogleStatus(status)
	if status == http.StatusServiceUnavailable {
		googleStatus = "UNAVAILABLE"
	}
	requestID := ""
	if c != nil && c.Request != nil {
		requestID = contentModerationRequestID(c.Request.Context())
	}
	c.JSON(status, gin.H{"error": gin.H{
		"code": status, "message": securityAuditMessage(decision), "status": googleStatus,
		"details": []gin.H{{
			"@type":  "type.googleapis.com/google.rpc.ErrorInfo",
			"reason": securityAuditErrorCode(decision), "domain": "sub2api.securityaudit",
			"metadata": gin.H{"request_id": requestID},
		}},
	}})
}

func writeSecurityAuditWSError(ctx context.Context, conn *coderws.Conn, decision *securityaudit.Decision) {
	if conn == nil || decision == nil {
		return
	}
	if decision.Legacy != nil && decision.Legacy.Blocked {
		legacy := decision.Legacy
		writeContentModerationWSError(ctx, conn, (legacyContentModerationDecision{legacy}).toService())
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	payload, err := json.Marshal(gin.H{
		"event_id": "evt_prompt_guard_rejected", "type": "error",
		"error": gin.H{"type": "invalid_request_error", "code": securityAuditErrorCode(decision), "message": securityAuditMessage(decision)},
	})
	if err != nil {
		return
	}
	writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_ = conn.Write(writeCtx, coderws.MessageText, payload)
}

type legacyContentModerationDecision struct{ value *securityaudit.LegacyDecision }

func (d legacyContentModerationDecision) toService() *service.ContentModerationDecision {
	if d.value == nil {
		return nil
	}
	return &service.ContentModerationDecision{Allowed: d.value.Allowed, Blocked: d.value.Blocked, Flagged: d.value.Flagged, Message: d.value.Message, StatusCode: d.value.StatusCode, Action: d.value.Action}
}

func securityAuditWSCloseStatus(decision *securityaudit.Decision) coderws.StatusCode {
	if decision == nil {
		return coderws.StatusInternalError
	}
	if decision.Legacy != nil && decision.Legacy.Blocked {
		return coderws.StatusPolicyViolation
	}
	if decision.Kind == securityaudit.DecisionBlock {
		return coderws.StatusCode(4403)
	}
	return coderws.StatusTryAgainLater
}

func securityAuditWSCloseReason(decision *securityaudit.Decision) string {
	if decision == nil {
		return securityaudit.ErrorCodeUnavailable
	}
	if decision.Legacy != nil && decision.Legacy.Blocked {
		message := strings.TrimSpace(decision.Legacy.Message)
		if message != "" {
			return message
		}
		return "content_policy_violation"
	}
	code := securityAuditErrorCode(decision)
	if code == "" {
		return securityaudit.ErrorCodeUnavailable
	}
	return code
}
