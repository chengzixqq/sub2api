package service

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
)

// Account error kinds give clients a stable classification of the free-form
// error_message written by the many account failure paths.
const (
	AccountErrorKindAuth          = "auth"
	AccountErrorKindForbidden     = "forbidden"
	AccountErrorKindQuota         = "quota"
	AccountErrorKindCustomCode    = "custom_code"
	AccountErrorKindStreamTimeout = "stream_timeout"
	AccountErrorKindTokenRefresh  = "token_refresh"
	AccountErrorKindPrivacy       = "privacy"
	AccountErrorKindRateLimit     = "rate_limit"
	AccountErrorKindOther         = "other"

	accountErrorSummaryMaxRunes = 160
)

// AccountErrorDescription is the structured view derived from error_message.
type AccountErrorDescription struct {
	Kind       string
	StatusCode *int
	Summary    string
}

var (
	accountErrorStatusPattern = regexp.MustCompile(`\((\d{3})\)|\b(?:returned|error code|OAuth|INTERNAL)\s+(\d{3})\b`)
	accountErrorHTMLTag       = regexp.MustCompile(`<[^>]*>`)
	accountErrorSecretKey     = regexp.MustCompile(`\b(sk-[A-Za-z0-9]{1,8})[A-Za-z0-9_\-]{8,}`)
	accountErrorSpaces        = regexp.MustCompile(`\s+`)
)

// accountErrorKindPrefixes is ordered: the first matching prefix wins.
var accountErrorKindPrefixes = []struct {
	prefix string
	kind   string
}{
	{"authentication failed", AccountErrorKindAuth},
	{"unauthorized", AccountErrorKindAuth},
	{"token revoked", AccountErrorKindAuth},
	{"oauth 401", AccountErrorKindAuth},
	{"access forbidden", AccountErrorKindForbidden},
	{"organization disabled", AccountErrorKindForbidden},
	{"identity verification required", AccountErrorKindForbidden},
	{"workspace deactivated", AccountErrorKindForbidden},
	{"credit balance exhausted", AccountErrorKindQuota},
	{"payment required", AccountErrorKindQuota},
	{"custom error code", AccountErrorKindCustomCode},
	{"stream data interval timeout", AccountErrorKindStreamTimeout},
	{"token refresh failed", AccountErrorKindTokenRefresh},
	{"privacy not set", AccountErrorKindPrivacy},
}

// DescribeAccountError classifies an account error message and produces a short
// redacted summary. The raw message stays available as error_message.
func DescribeAccountError(message string) AccountErrorDescription {
	message = strings.TrimSpace(message)
	if message == "" {
		return AccountErrorDescription{}
	}
	return AccountErrorDescription{
		Kind:       classifyAccountError(message),
		StatusCode: accountErrorStatusCode(message),
		Summary:    summarizeAccountError(message),
	}
}

func classifyAccountError(message string) string {
	lower := strings.ToLower(message)
	for _, entry := range accountErrorKindPrefixes {
		if strings.HasPrefix(lower, entry.prefix) {
			return entry.kind
		}
	}
	return AccountErrorKindOther
}

func accountErrorStatusCode(message string) *int {
	match := accountErrorStatusPattern.FindStringSubmatch(message)
	if match == nil {
		return nil
	}
	raw := match[1]
	if raw == "" {
		raw = match[2]
	}
	code, err := strconv.Atoi(raw)
	if err != nil || code < 100 || code > 599 {
		return nil
	}
	return &code
}

func summarizeAccountError(message string) string {
	return redactAccountErrorText(message, accountErrorSummaryMaxRunes)
}

// redactAccountErrorText masks credentials, upstream URLs and HTML in an error
// text, collapses whitespace and caps it at maxRunes.
func redactAccountErrorText(message string, maxRunes int) string {
	out := logredact.RedactText(message, "key", "token", "api_key", "x-api-key", "authorization")
	out = sanitizeUpstreamErrorMessage(out)
	out = redactUpstreamURLs(out)
	out = accountErrorSecretKey.ReplaceAllString(out, "$1***")
	out = accountErrorHTMLTag.ReplaceAllString(out, " ")
	out = strings.TrimSpace(accountErrorSpaces.ReplaceAllString(out, " "))
	runes := []rune(out)
	if len(runes) > maxRunes {
		out = string(runes[:maxRunes-1]) + "…"
	}
	return out
}
