package service

import (
	"net/http"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

// The App decides what to do from these reasons (docs/API_ERROR_CODES.md);
// renaming one silently breaks native clients.
func TestStableErrorReasons(t *testing.T) {
	cases := []struct {
		err    error
		status int
		reason string
	}{
		{ErrInvalidCredentials, http.StatusUnauthorized, "INVALID_CREDENTIALS"},
		{ErrInvalidToken, http.StatusUnauthorized, "INVALID_TOKEN"},
		{ErrTokenExpired, http.StatusUnauthorized, "TOKEN_EXPIRED"},
		{ErrTokenRevoked, http.StatusUnauthorized, "TOKEN_REVOKED"},
		{ErrRefreshTokenInvalid, http.StatusUnauthorized, "REFRESH_TOKEN_INVALID"},
		{ErrRefreshTokenExpired, http.StatusUnauthorized, "REFRESH_TOKEN_EXPIRED"},
		{ErrRefreshTokenReused, http.StatusUnauthorized, "REFRESH_TOKEN_REUSED"},
		{ErrSessionBindingMismatch, http.StatusUnauthorized, "SESSION_BINDING_MISMATCH"},
		{ErrUserNotActive, http.StatusForbidden, "USER_NOT_ACTIVE"},
		{ErrTotpInvalidCode, http.StatusBadRequest, "TOTP_INVALID_CODE"},
		{ErrTotpTooManyAttempts, http.StatusTooManyRequests, "TOTP_TOO_MANY_ATTEMPTS"},
		{ErrTotpLoginSessionExpired, http.StatusBadRequest, "TOTP_LOGIN_SESSION_EXPIRED"},
		{ErrBackendModeAdminOnly, http.StatusForbidden, "BACKEND_MODE_ADMIN_ONLY"},
		{ErrBackendModeSelfServiceDisabled, http.StatusForbidden, "BACKEND_MODE_SELF_SERVICE_DISABLED"},
		{ErrOpsDisabled, http.StatusNotFound, "OPS_DISABLED"},
		{ErrOpsTokenStatsRangeTooLarge, http.StatusBadRequest, "OPS_TIME_RANGE_TOO_LARGE"},
		{ErrServiceUnavailable, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE"},
		{ErrCaptchaProviderConflict, http.StatusServiceUnavailable, "CAPTCHA_PROVIDER_CONFLICT"},
		{ErrAdminAccessTokenInvalid, http.StatusUnauthorized, "INVALID_ADMIN_KEY"},
		{ErrAdminAccessTokenExpired, http.StatusUnauthorized, "ACCESS_TOKEN_EXPIRED"},
		{ErrAdminAccessTokenReadOnly, http.StatusForbidden, "ACCESS_TOKEN_READ_ONLY"},
		{ErrSessionNotFound, http.StatusNotFound, "SESSION_NOT_FOUND"},
		{ErrTooManyUsageUserIDs, http.StatusBadRequest, "TOO_MANY_IDS"},
		{ErrTooManyUsageAPIKeyIDs, http.StatusBadRequest, "TOO_MANY_IDS"},
	}
	for _, tc := range cases {
		require.Equal(t, tc.reason, infraerrors.Reason(tc.err))
		require.Equal(t, tc.status, infraerrors.Code(tc.err), tc.reason)
	}
}
