package service

import (
	"errors"
	"net/http"
	"strconv"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/oauth"
)

// OAuth code-exchange failures share one shape across platforms so clients can
// branch on the reason suffix: <PLATFORM>_OAUTH_SESSION_NOT_FOUND (link expired,
// regenerate it), <PLATFORM>_OAUTH_INVALID_STATE (code belongs to another link)
// and <PLATFORM>_OAUTH_CODE_REJECTED (upstream refused the code;
// metadata.upstream_status carries its HTTP status). OpenAI and Grok build the
// first two inline in their services. Messages keep each platform's historical
// wording so the Web UI shows the same text as before.
var (
	ErrClaudeOAuthSessionNotFound = infraerrors.New(http.StatusBadRequest, "CLAUDE_OAUTH_SESSION_NOT_FOUND", "session not found or expired")
	ErrClaudeOAuthInvalidState    = infraerrors.New(http.StatusBadRequest, "CLAUDE_OAUTH_INVALID_STATE", "invalid oauth state")

	ErrGeminiOAuthSessionNotFound = infraerrors.New(http.StatusBadRequest, "GEMINI_OAUTH_SESSION_NOT_FOUND", "session not found or expired")
	ErrGeminiOAuthInvalidState    = infraerrors.New(http.StatusBadRequest, "GEMINI_OAUTH_INVALID_STATE", "invalid state")

	ErrAntigravityOAuthSessionNotFound = infraerrors.New(http.StatusBadRequest, "ANTIGRAVITY_OAUTH_SESSION_NOT_FOUND", "session 不存在或已过期")
	ErrAntigravityOAuthInvalidState    = infraerrors.New(http.StatusBadRequest, "ANTIGRAVITY_OAUTH_INVALID_STATE", "state 无效")
)

const (
	ClaudeOAuthCodeRejectedReason      = "CLAUDE_OAUTH_CODE_REJECTED"
	GeminiOAuthCodeRejectedReason      = "GEMINI_OAUTH_CODE_REJECTED"
	AntigravityOAuthCodeRejectedReason = "ANTIGRAVITY_OAUTH_CODE_REJECTED"
	OpenAIOAuthCodeRejectedReason      = "OPENAI_OAUTH_CODE_REJECTED"
	GrokOAuthCodeRejectedReason        = "GROK_OAUTH_CODE_REJECTED"

	// claudeOAuthCodeRejectedMessage is fixed: Claude never echoed upstream
	// token-endpoint bodies to clients, and the code exchange keeps it that way.
	claudeOAuthCodeRejectedMessage = "authorization code was rejected by upstream"
)

// NewOAuthCodeRejectedError builds the 400 returned when an OAuth token
// endpoint refuses an authorization code.
func NewOAuthCodeRejectedError(reason, message string, upstreamStatus int) *infraerrors.ApplicationError {
	return infraerrors.New(http.StatusBadRequest, reason, message).
		WithMetadata(map[string]string{"upstream_status": strconv.Itoa(upstreamStatus)})
}

// oauthCodeRejected converts a token-exchange error carrying an upstream
// response into the platform's CODE_REJECTED error when oauth.IsCodeRejected
// says the code was refused. Transient statuses, outages, blocked requests,
// client misconfiguration and transport errors return ok=false so callers keep
// their previous behavior.
func oauthCodeRejected(reason, message string, err error) (error, bool) {
	var upstreamErr interface {
		UpstreamStatusCode() int
		UpstreamBody() string
	}
	if !errors.As(err, &upstreamErr) {
		return nil, false
	}
	status := upstreamErr.UpstreamStatusCode()
	if !oauth.IsCodeRejected(status, upstreamErr.UpstreamBody()) {
		return nil, false
	}
	return NewOAuthCodeRejectedError(reason, message, status).WithCause(err), true
}
