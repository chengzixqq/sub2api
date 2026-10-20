package oauth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// TokenExchangeError reports a non-2xx response from an OAuth token endpoint
// during authorization-code exchange. Its text matches the historical
// fmt.Errorf message so existing logs and Web messages stay unchanged, while
// UpstreamStatusCode and UpstreamBody let callers tell a rejected code from an
// outage or a blocked request.
type TokenExchangeError struct {
	StatusCode int
	Body       string
}

func (e *TokenExchangeError) Error() string {
	return fmt.Sprintf("token exchange failed: status %d, body: %s", e.StatusCode, e.Body)
}

// UpstreamStatusCode returns the HTTP status the token endpoint answered with.
func (e *TokenExchangeError) UpstreamStatusCode() int {
	return e.StatusCode
}

// UpstreamBody returns the token endpoint's response body.
func (e *TokenExchangeError) UpstreamBody() string {
	return e.Body
}

// OAuth error codes (RFC 6749 §5.2) that blame the client registration or the
// request shape rather than the authorization code; a new code cannot fix them.
var oauthClientErrorCodes = map[string]struct{}{
	"invalid_client":         {},
	"unauthorized_client":    {},
	"unsupported_grant_type": {},
	"invalid_scope":          {},
}

// IsCodeRejected reports whether a failed token-endpoint response means the
// authorization code itself was refused (expired, already used, wrong
// verifier...), so the user has to authorize again.
//
//   - 408, 429 and 5xx are transient: the same code may still succeed.
//   - An "invalid_grant" error refuses the code at any other 4xx status (some
//     providers answer it with 401 or 403).
//   - invalid_client, unauthorized_client, unsupported_grant_type and
//     invalid_scope are client configuration problems, not the code.
//   - Otherwise only a plain 400 counts. 401 / 403 / 404 without invalid_grant
//     are usually a WAF challenge, a region block or a misconfigured client,
//     where the code is still unused and can be retried once that is fixed.
func IsCodeRejected(status int, body string) bool {
	if status < http.StatusBadRequest || status >= http.StatusInternalServerError {
		return false
	}
	if status == http.StatusRequestTimeout || status == http.StatusTooManyRequests {
		return false
	}
	code := oauthErrorCode(body)
	if code == "invalid_grant" {
		return true
	}
	if _, ok := oauthClientErrorCodes[code]; ok {
		return false
	}
	return status == http.StatusBadRequest
}

// oauthErrorCode extracts the RFC 6749 "error" string from a token-endpoint
// response body, lower-cased; it returns "" when the body carries none.
func oauthErrorCode(body string) string {
	var payload struct {
		Error any `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return ""
	}
	code, _ := payload.Error.(string)
	return strings.ToLower(strings.TrimSpace(code))
}
