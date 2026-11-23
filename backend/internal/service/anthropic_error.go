package service

import (
	"encoding/json"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
)

// IsAnthropicMessagesRequest selects the inbound protocol independently of the
// provider chosen to serve it. Direct handler calls have no Gin route pattern.
func IsAnthropicMessagesRequest(c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.Method != http.MethodPost {
		return false
	}
	path := c.FullPath()
	if path == "" && c.Request.URL != nil {
		path = c.Request.URL.Path
	}
	switch path {
	case "/v1/messages", "/v1/messages/count_tokens", "/messages/count_tokens",
		"/antigravity/v1/messages", "/antigravity/v1/messages/count_tokens":
		return true
	default:
		return false
	}
}

const OpsAnthropicErrorTypeKey = "ops_anthropic_original_error_type"
const OpsAnthropicErrorMessageKey = "ops_anthropic_original_error_message"
const opsNextResponseKey = "ops_next_original_error_response"
const opsOriginalResponseKey = "ops_original_error_response"

type opsOriginalResponse struct {
	payload     any
	framePrefix string
}

// PreserveOpsErrorResponse describes the response the original writer produced.
// An empty prefix denotes ordinary JSON, including JSON appended after a ping.
func PreserveOpsErrorResponse(c *gin.Context, payload any, framePrefix string) {
	c.Set(opsNextResponseKey, opsOriginalResponse{payload, framePrefix})
}

// Gin's original JSON envelopes used lexically ordered map keys. The order
// also matters when the capture limit falls in the middle of a long message.
func OpsClaudeErrorPayload(errType, code, message string) any {
	detail := gin.H{"message": message, "type": errType}
	if code != "" {
		detail["code"] = code
	}
	return gin.H{"error": detail, "type": "error"}
}

// These service writers historically appended unframed JSON after a ping.
func preserveOpsClaudeJSON(c *gin.Context, errType, message string) {
	if IsAnthropicMessagesRequest(c) {
		PreserveOpsErrorResponse(c, OpsClaudeErrorPayload(errType, "", message), "")
	}
}

// The stream reader used to emit its reason and the handler then wrote a
// second, data-only upstream_error. Keep both payloads on the Ops side.
func markOpsAnthropicTerminalError(c *gin.Context, reason string, originalPayload any) {
	const message = "Upstream request failed"
	detail := reason
	if c.GetString(OpsUpstreamErrorDetailKey) != "" {
		detail = ""
	}
	SetOpsUpstreamError(c, http.StatusBadGateway, message, detail)
	c.Set(OpsTerminalStreamReasonKey, reason)
	MarkOpsStreamError(c, "upstream_error", message, http.StatusBadGateway)
	c.Set(opsOriginalResponseKey, opsOriginalResponse{originalPayload, "event: error\ndata: "})
}

func OpsOriginalErrorMessage(c *gin.Context, fallback string) string {
	if value, exists := c.Get(OpsAnthropicErrorMessageKey); exists {
		if message, ok := value.(string); ok {
			return message
		}
	}
	return fallback
}

func OpsOriginalResponseBody(c *gin.Context) ([]byte, bool) {
	value, _ := c.Get(opsOriginalResponseKey)
	response, ok := value.(opsOriginalResponse)
	if !ok {
		return nil, false
	}
	if response.framePrefix == "" && gatewaySSEStarted(c) {
		return nil, true
	}
	payload, err := json.Marshal(response.payload)
	if err != nil {
		return nil, false
	}
	if response.framePrefix != "" {
		payload = []byte(response.framePrefix + string(payload) + "\n\n")
	}
	return payload, true
}

// PreserveOpsOpenAIError retains the original OpenAI envelope when the caller
// adapts a local failure to the Messages protocol.
func PreserveOpsOpenAIError(c *gin.Context, errType, code, message string, stream bool) {
	detail := gin.H{"type": errType, "message": OpsOriginalErrorMessage(c, message)}
	if code != "" {
		detail["code"] = code
	}
	prefix := ""
	if stream {
		prefix = "event: error\ndata: "
	}
	PreserveOpsErrorResponse(c, gin.H{"error": detail}, prefix)
}

// MessagesErrorType adapts locally written errors at the wire boundary.
// Keep the original classification for Ops, including SSE status inference.
func MessagesErrorType(c *gin.Context, status int, original string) string {
	if !IsAnthropicMessagesRequest(c) {
		return original
	}
	c.Set(OpsAnthropicErrorTypeKey, original)
	return claude.ErrorType(status)
}
