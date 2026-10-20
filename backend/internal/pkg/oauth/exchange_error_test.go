package oauth

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTokenExchangeError_TextAndStatus(t *testing.T) {
	err := &TokenExchangeError{StatusCode: http.StatusBadRequest, Body: "bad request"}

	// Callers (Web, logs) have always seen this exact text; it must not drift.
	require.Equal(t, "token exchange failed: status 400, body: bad request", err.Error())
	require.Equal(t, http.StatusBadRequest, err.UpstreamStatusCode())
	require.Equal(t, "bad request", err.UpstreamBody())

	var typed *TokenExchangeError
	require.True(t, errors.As(fmt.Errorf("wrapped: %w", err), &typed))
	require.Equal(t, http.StatusBadRequest, typed.StatusCode)
}

func TestIsCodeRejected(t *testing.T) {
	const cloudflare = `<!DOCTYPE html><html><head><title>Just a moment...</title></head><body>cf challenge</body></html>`
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"no status", 0, "", false},
		{"success", http.StatusOK, "", false},
		{"below 4xx", 399, "", false},

		// RFC 6749 invalid_grant: the code itself was refused, whatever 4xx carries it.
		{"400 invalid_grant", http.StatusBadRequest, `{"error":"invalid_grant","error_description":"Invalid 'code' in request."}`, true},
		{"401 invalid_grant", http.StatusUnauthorized, `{"error":"invalid_grant"}`, true},
		{"403 invalid_grant", http.StatusForbidden, `{"error":"invalid_grant","error_description":"Invalid authorization code"}`, true},
		{"invalid_grant case and spaces", http.StatusBadRequest, `{"error":" Invalid_Grant "}`, true},

		// Plain 400 without a client/config error code still means the code was refused.
		{"400 empty body", http.StatusBadRequest, "", true},
		{"400 text body", http.StatusBadRequest, "bad request", true},
		{"400 invalid_request", http.StatusBadRequest, `{"error":"invalid_request"}`, true},
		{"400 nested error object", http.StatusBadRequest, `{"type":"error","error":{"type":"invalid_request_error","message":"Invalid request format"}}`, true},

		// Client / configuration problems: re-authorizing never helps.
		{"401 invalid_client", http.StatusUnauthorized, `{"error":"invalid_client","error_description":"Unauthorized"}`, false},
		{"400 invalid_client", http.StatusBadRequest, `{"error":"invalid_client"}`, false},
		{"400 unauthorized_client", http.StatusBadRequest, `{"error":"unauthorized_client"}`, false},
		{"400 unsupported_grant_type", http.StatusBadRequest, `{"error":"unsupported_grant_type"}`, false},
		{"400 invalid_scope", http.StatusBadRequest, `{"error":"invalid_scope"}`, false},

		// 401 / 403 / 404 without invalid_grant: WAF, region block, misrouting; the code is still unused.
		{"403 cloudflare html", http.StatusForbidden, cloudflare, false},
		{"403 empty", http.StatusForbidden, "", false},
		{"401 empty", http.StatusUnauthorized, "", false},
		{"401 nested error object", http.StatusUnauthorized, `{"type":"error","error":{"type":"authentication_error"}}`, false},
		{"404 not found", http.StatusNotFound, "not found", false},

		// Transient statuses and outages never count, even with invalid_grant.
		{"408 invalid_grant", http.StatusRequestTimeout, `{"error":"invalid_grant"}`, false},
		{"429 invalid_grant", http.StatusTooManyRequests, `{"error":"invalid_grant"}`, false},
		{"500 invalid_grant", http.StatusInternalServerError, `{"error":"invalid_grant"}`, false},
		{"502", http.StatusBadGateway, "", false},
		{"503", http.StatusServiceUnavailable, "", false},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, IsCodeRejected(tc.status, tc.body), tc.name)
	}
}
