package service

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
)

const redactUpstreamURLContextKey = "claude_customization_redact_upstream_url"

var upstreamURLPattern = regexp.MustCompile(`https?://[^\s"'<>]+`)

// redactUpstreamURL preserves the scheme and path while hiding the host. Query
// and fragment values are always removed because they may contain credentials.
func redactUpstreamURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return safeUpstreamURL(raw)
	}
	return u.Scheme + "://***.***" + u.EscapedPath()
}

func redactUpstreamURLs(text string) string {
	return upstreamURLPattern.ReplaceAllStringFunc(text, redactUpstreamURL)
}

func sanitizeUpstreamErrorMessageForContext(c *gin.Context, message string) string {
	if c != nil {
		if redact, _ := c.Get(redactUpstreamURLContextKey); redact == true {
			return redactUpstreamURLs(message)
		}
	}
	return message
}

// SanitizeUpstreamErrorMessageForContext exposes the request-scoped URL
// redaction helper to handler packages that build client-facing failover errors.
func SanitizeUpstreamErrorMessageForContext(c *gin.Context, message string) string {
	return sanitizeUpstreamErrorMessageForContext(c, message)
}

// redactUpstreamResponseBodyForClient returns a copy suitable for a downstream
// error response. Internal classification and accounting must continue using
// the original body; only the client-visible representation is redacted.
func redactUpstreamResponseBodyForClient(c *gin.Context, body []byte) []byte {
	if c == nil {
		return body
	}
	if redact, _ := c.Get(redactUpstreamURLContextKey); redact == true {
		return []byte(redactUpstreamURLs(string(body)))
	}
	return body
}
