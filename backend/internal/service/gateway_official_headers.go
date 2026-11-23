package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
	"regexp"
)

// Only these published rate-limit dimensions may cross the Claude boundary.
var officialResponseHeaderNames = [...]string{
	"Request-Id", "Retry-After",
	"Anthropic-Ratelimit-Requests-Limit", "Anthropic-Ratelimit-Requests-Remaining", "Anthropic-Ratelimit-Requests-Reset",
	"Anthropic-Ratelimit-Tokens-Limit", "Anthropic-Ratelimit-Tokens-Remaining", "Anthropic-Ratelimit-Tokens-Reset",
	"Anthropic-Ratelimit-Input-Tokens-Limit", "Anthropic-Ratelimit-Input-Tokens-Remaining", "Anthropic-Ratelimit-Input-Tokens-Reset",
	"Anthropic-Ratelimit-Output-Tokens-Limit", "Anthropic-Ratelimit-Output-Tokens-Remaining", "Anthropic-Ratelimit-Output-Tokens-Reset",
}

// Forward and ForwardCountTokens already resolved the current account's overrides.
func cachedClaudeResponsePolicy(c *gin.Context) ClaudeCustomizationSettings {
	value, _ := c.Get(claudeCustomizationRequestContextKey)
	cached, _ := value.(claudeCustomizationRequestPolicy)
	return cached.policy
}

func applyOfficialResponseHeaders(c *gin.Context, policy ClaudeCustomizationSettings, upstream http.Header) {
	if policy.UpstreamHeaderPassthrough != ClaudeHeaderPassthroughOn || !IsAnthropicMessagesRequest(c) || c.Writer.Written() {
		return
	}
	// A previous attempt's legacy response filter may have allowed these headers.
	dst := c.Writer.Header()
	dst.Del("Anthropic-Organization-Id")
	blocked := c.GetStringSlice(officialForceRemoveKey)
	for _, name := range officialResponseHeaderNames {
		dst.Del(name)
		if values := upstream.Values(name); len(values) != 0 && !slices.ContainsFunc(blocked, func(key string) bool { return strings.EqualFold(strings.TrimSpace(key), name) }) {
			for _, v := range values {
				dst.Add(name, v)
			}
		}
	}
}

const filledRequestIDKey = "anthropic_filled_request_id"
const officialForceRemoveKey = "anthropic_official_force_remove"

// FilledAnthropicRequestID is set only when fill chose the ID for the client response.
func FilledAnthropicRequestID(c *gin.Context) string {
	return c.GetString(filledRequestIDKey)
}

func generateResponseRequestID(c *gin.Context) string {
	id := NewAnthropicRequestID()
	logger.FromContext(c.Request.Context()).Info("generated Anthropic request ID", zap.String("anthropic_request_id", id))
	return id
}

var officialRequestID = regexp.MustCompile(`^req_[0-9A-Za-z]{1,64}$`)

func selectResponseRequestID(c *gin.Context, policy ClaudeCustomizationSettings, header http.Header, errorBody []byte) string {
	if policy.RequestIDMode != ClaudeRequestIDFill || !IsAnthropicMessagesRequest(c) {
		return ""
	}
	if c.Writer.Written() {
		return FilledAnthropicRequestID(c)
	}
	id := header.Get("Request-Id")
	if id == "" && len(errorBody) > 0 {
		if field := gjson.GetBytes(errorBody, "request_id"); field.Type == gjson.String {
			id = field.Str
		}
	}
	if len(id) > 68 || !officialRequestID.MatchString(id) {
		id = generateResponseRequestID(c)
	}
	c.Header("Request-Id", id)
	c.Set(filledRequestIDKey, id)
	return id
}

func fillRequestID(c *gin.Context, policy ClaudeCustomizationSettings, upstreamHeader http.Header, errorBody []byte) []byte {
	id := selectResponseRequestID(c, policy, upstreamHeader, errorBody)
	return appendResponseRequestID(errorBody, id)
}

func appendResponseRequestID(body []byte, id string) []byte {
	if id == "" || len(body) == 0 {
		return body
	}
	if field := gjson.GetBytes(body, "request_id"); field.Type == gjson.String && field.Str == id {
		return body
	}
	// Non-JSON upstream errors have no object envelope. Preserve their diagnostic bytes.
	var compact bytes.Buffer
	if err := json.Compact(&compact, body); err != nil || compact.Bytes()[0] != '{' {
		return body
	}
	filled, err := sjson.SetBytes(compact.Bytes(), "request_id", id)
	if err != nil {
		return body
	}
	return filled
}

func prepareOfficialResponse(c *gin.Context, policy ClaudeCustomizationSettings, headers http.Header, upstreamError []byte) {
	applyOfficialResponseHeaders(c, policy, headers)
	selectResponseRequestID(c, policy, headers, upstreamError)
}

// pendingOfficialStreamHeaders publishes headers only at the first write, so
// an early stream failover cannot leak one account's headers into the next attempt.
func pendingOfficialStreamHeaders(c *gin.Context, headers http.Header) func() {
	policy := cachedClaudeResponsePolicy(c)
	if !officialResponseEnabled(policy) {
		return func() {}
	}
	mode, passthrough := policy.RequestIDMode, policy.UpstreamHeaderPassthrough
	return func() {
		if !c.Writer.Written() {
			prepareOfficialResponse(c, ClaudeCustomizationSettings{RequestIDMode: mode, UpstreamHeaderPassthrough: passthrough}, headers, nil)
		}
	}
}

func officialResponseEnabled(policy ClaudeCustomizationSettings) bool {
	return policy.RequestIDMode == ClaudeRequestIDFill || policy.UpstreamHeaderPassthrough == ClaudeHeaderPassthroughOn
}

// Preserve the failed attempt's policy and headers until the handler has
// decided whether to retry or to render its final error.
func (e *UpstreamFailoverError) withOfficialHeaders(c *gin.Context, upstream http.Header) *UpstreamFailoverError {
	policy := cachedClaudeResponsePolicy(c)
	if officialResponseEnabled(policy) && IsAnthropicMessagesRequest(c) {
		e.ResponseHeaders = upstream
		e.officialRequestIDMode = policy.RequestIDMode
		e.officialHeaderPassthrough = policy.UpstreamHeaderPassthrough
	}
	return e
}

func (e *UpstreamFailoverError) ApplyOfficialResponseHeaders(c *gin.Context) {
	policy := ClaudeCustomizationSettings{RequestIDMode: e.officialRequestIDMode, UpstreamHeaderPassthrough: e.officialHeaderPassthrough}
	if !officialResponseEnabled(policy) {
		policy = cachedClaudeResponsePolicy(c)
	}
	prepareOfficialResponse(c, policy, e.ResponseHeaders, nil)
}

func (s *SettingService) globalAuthRequestIDFill(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return false
	}
	snapshot := s.alignmentRejectCache.Load()
	s.tryRefreshAlignmentSnapshot(ctx, snapshot)
	return snapshot != nil && snapshot.global.RequestIDMode == ClaudeRequestIDFill
}

// FillAnthropicAuthRequestID uses only global settings before account selection.
// The auth middleware selects the protocol, including Anthropic GET /v1/models.
func (s *SettingService) FillAnthropicAuthRequestID(c *gin.Context) string {
	if !s.globalAuthRequestIDFill(c.Request.Context()) {
		return ""
	}
	id := NewAnthropicRequestID()
	c.Header("Request-Id", id)
	return id
}

// BindClaudeErrorSettings supplies the global snapshot for pre-selection
// writers. An account policy cached during forwarding always takes precedence.
func BindClaudeErrorSettings(c *gin.Context, s *SettingService) {
	if s != nil {
		c.Set("claude_error_settings", s)
	}
}
func ClaudeLocalErrorID(c *gin.Context) string {
	value, _ := c.Get("claude_error_settings")
	s, _ := value.(*SettingService)
	// The middleware binds settings on authenticated Claude models routes too.
	// Header passthrough and upstream ID selection are specific to Messages.
	if s != nil && c.Request.Method == http.MethodGet && !c.Writer.Written() && s.globalAuthRequestIDFill(c.Request.Context()) {
		return SetLocalAnthropicRequestID(c)
	}
	return s.FillClaudeHandlerErrorID(c)
}

// FillClaudeHandlerErrorID chooses the account policy once a forward attempt has
// started. Before selection only the global snapshot can apply.
func (s *SettingService) FillClaudeHandlerErrorID(c *gin.Context) string {
	if id := FilledAnthropicRequestID(c); id != "" {
		return id
	}
	if c.Writer.Written() || !IsAnthropicMessagesRequest(c) {
		return ""
	}
	policy := cachedClaudeResponsePolicy(c)
	if _, selected := c.Get(claudeCustomizationRequestContextKey); !selected && s.globalAuthRequestIDFill(c.Request.Context()) {
		policy.RequestIDMode = ClaudeRequestIDFill
	}
	return selectResponseRequestID(c, policy, nil, nil)
}

// SetLocalAnthropicRequestID publishes a generated ID before writing a local response.
func SetLocalAnthropicRequestID(c *gin.Context) string {
	id := generateResponseRequestID(c)
	c.Header("Request-Id", id)
	return id
}

// AnthropicErrorWithRequestID preserves the error envelope and appends its ID last.
func AnthropicErrorWithRequestID(errType, message, id string) any {
	return AnthropicErrorWithCodeAndRequestID(errType, "", message, id)
}

func AnthropicErrorWithCodeAndRequestID(errType, code, message, id string) any {
	type detail struct {
		Type    string `json:"type"`
		Code    string `json:"code,omitempty"`
		Message string `json:"message"`
	}
	return struct {
		Type      string `json:"type"`
		Error     detail `json:"error"`
		RequestID string `json:"request_id,omitempty"`
	}{"error", detail{errType, code, message}, id}
}
