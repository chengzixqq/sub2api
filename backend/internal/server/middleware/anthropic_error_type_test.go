//go:build unit

package middleware

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAnthropicErrorType(t *testing.T) {
	for _, tc := range []struct {
		status int
		typ    string
	}{
		{400, "invalid_request_error"}, {401, "authentication_error"}, {402, "billing_error"},
		{403, "permission_error"}, {404, "not_found_error"}, {408, "invalid_request_error"},
		{409, "conflict_error"}, {413, "request_too_large"}, {422, "api_error"},
		{429, "rate_limit_error"}, {499, "invalid_request_error"}, {500, "api_error"},
		{502, "api_error"}, {503, "api_error"}, {504, "timeout_error"}, {529, "overloaded_error"}, {599, "api_error"},
	} {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) { require.Equal(t, tc.typ, AnthropicErrorType(tc.status)) })
	}
}
