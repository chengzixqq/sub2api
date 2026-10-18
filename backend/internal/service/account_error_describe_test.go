package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDescribeAccountError(t *testing.T) {
	cases := []struct {
		msg    string
		kind   string
		status int
	}{
		{"Authentication failed (401): invalid x-api-key", AccountErrorKindAuth, 401},
		{"Unauthorized (401): account authentication failed permanently", AccountErrorKindAuth, 401},
		{"Token revoked (401): account authentication permanently revoked", AccountErrorKindAuth, 401},
		{"OAuth 401 (no refresh_token): bad token", AccountErrorKindAuth, 401},
		{"OAuth 401: expired", AccountErrorKindAuth, 401},
		{"Access forbidden (403): account may be suspended or lack permissions", AccountErrorKindForbidden, 403},
		{"Organization disabled (400): This organization has been disabled.", AccountErrorKindForbidden, 400},
		{"Identity verification required (400): verify", AccountErrorKindForbidden, 400},
		{"Credit balance exhausted (400): Your credit balance is too low", AccountErrorKindQuota, 400},
		{"Payment required (402): insufficient balance or billing issue", AccountErrorKindQuota, 402},
		{"Workspace deactivated (402): workspace has been deactivated", AccountErrorKindForbidden, 402},
		{"Custom error code 529: overloaded", AccountErrorKindCustomCode, 529},
		{"Stream data interval timeout (repeated failures) for model: claude-sonnet-4-5", AccountErrorKindStreamTimeout, 0},
		{"Token refresh failed (non-retryable): invalid_grant", AccountErrorKindTokenRefresh, 0},
		{"Privacy not set, required by group [高级组]", AccountErrorKindPrivacy, 0},
		{"INTERNAL 500 consecutive failures: 3 rounds", AccountErrorKindOther, 500},
		{"something nobody anticipated", AccountErrorKindOther, 0},
	}
	for _, tc := range cases {
		t.Run(tc.msg, func(t *testing.T) {
			got := DescribeAccountError(tc.msg)
			require.Equal(t, tc.kind, got.Kind)
			if tc.status == 0 {
				require.Nil(t, got.StatusCode)
			} else {
				require.NotNil(t, got.StatusCode)
				require.Equal(t, tc.status, *got.StatusCode)
			}
			require.NotEmpty(t, got.Summary)
		})
	}
}

func TestDescribeAccountError_SummaryIsRedactedAndShort(t *testing.T) {
	msg := "Authentication failed (401): <html><body>bad key sk-ant-api03-SECRETSECRETSECRET for https://api.example.com/v1?key=AIzaSECRET123&x=1</body></html>" + strings.Repeat(" padding", 60)
	got := DescribeAccountError(msg)

	require.NotContains(t, got.Summary, "<html>")
	require.NotContains(t, got.Summary, "AIzaSECRET123")
	require.LessOrEqual(t, len([]rune(got.Summary)), 160)
	require.Equal(t, AccountErrorKindAuth, got.Kind)
}

func TestDescribeAccountError_Empty(t *testing.T) {
	got := DescribeAccountError("  ")
	require.Equal(t, "", got.Kind)
	require.Nil(t, got.StatusCode)
	require.Equal(t, "", got.Summary)
}
