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

	var typed *TokenExchangeError
	require.True(t, errors.As(fmt.Errorf("wrapped: %w", err), &typed))
	require.Equal(t, http.StatusBadRequest, typed.StatusCode)
}

func TestIsCodeRejectedStatus(t *testing.T) {
	cases := map[int]bool{
		0:                                        false,
		http.StatusOK:                            false,
		399:                                      false,
		http.StatusBadRequest:                    true,
		http.StatusUnauthorized:                  true,
		http.StatusForbidden:                     true,
		http.StatusNotFound:                      true,
		http.StatusRequestTimeout:                false,
		http.StatusTooManyRequests:               false,
		499:                                      true,
		http.StatusInternalServerError:           false,
		http.StatusBadGateway:                    false,
		http.StatusServiceUnavailable:            false,
		http.StatusGatewayTimeout:                false,
		http.StatusNetworkAuthenticationRequired: false,
	}
	for status, want := range cases {
		require.Equal(t, want, IsCodeRejectedStatus(status), "status %d", status)
	}
}
