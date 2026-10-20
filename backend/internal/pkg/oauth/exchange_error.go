package oauth

import (
	"fmt"
	"net/http"
)

// TokenExchangeError reports a non-2xx response from an OAuth token endpoint
// during authorization-code exchange. Its text matches the historical
// fmt.Errorf message so existing logs and Web messages stay unchanged, while
// UpstreamStatusCode lets callers tell a rejected code from an outage.
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

// IsCodeRejectedStatus reports whether a token endpoint status means the
// authorization code itself was refused (expired, already used, wrong
// verifier...). 408 and 429 are transient, and 5xx is an upstream outage, so
// retrying the same code may still succeed in those cases.
func IsCodeRejectedStatus(status int) bool {
	if status < http.StatusBadRequest || status >= http.StatusInternalServerError {
		return false
	}
	return status != http.StatusRequestTimeout && status != http.StatusTooManyRequests
}
