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
		{ErrOpsConfigDisabled, http.StatusConflict, "OPS_CONFIG_DISABLED"},
		{ErrOpsTokenStatsRangeTooLarge, http.StatusBadRequest, "OPS_TIME_RANGE_TOO_LARGE"},
		{ErrOpsSystemMetricsHistoryRangeTooLarge, http.StatusBadRequest, "OPS_TIME_RANGE_TOO_LARGE"},
		{ErrOpsRequestNotFound, http.StatusNotFound, "OPS_REQUEST_NOT_FOUND"},
		{ErrOpsRequestAmbiguous, http.StatusConflict, "OPS_REQUEST_AMBIGUOUS"},
		{newOpsRequestAmbiguousError([]string{"a", "b"}), http.StatusConflict, "OPS_REQUEST_AMBIGUOUS"},
		{ErrServiceUnavailable, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE"},
		{ErrCaptchaProviderConflict, http.StatusServiceUnavailable, "CAPTCHA_PROVIDER_CONFLICT"},
		{ErrAdminAccessTokenInvalid, http.StatusUnauthorized, "INVALID_ADMIN_KEY"},
		{ErrAdminAccessTokenExpired, http.StatusUnauthorized, "ACCESS_TOKEN_EXPIRED"},
		{ErrAdminAccessTokenReadOnly, http.StatusForbidden, "ACCESS_TOKEN_READ_ONLY"},
		{ErrSessionNotFound, http.StatusNotFound, "SESSION_NOT_FOUND"},
		{ErrTooManyUsageUserIDs, http.StatusBadRequest, "TOO_MANY_IDS"},
		{ErrTooManyUsageAPIKeyIDs, http.StatusBadRequest, "TOO_MANY_IDS"},
		{ErrDashboardHomeInvalidTimezone, http.StatusBadRequest, "INVALID_TIMEZONE"},
		{ErrDashboardHomeInvalidInclude, http.StatusBadRequest, "INVALID_INCLUDE"},
		{ErrAccountTestRunning, http.StatusConflict, "ACCOUNT_TEST_RUNNING"},
		{ErrAccountTestRunNotFound, http.StatusNotFound, "ACCOUNT_TEST_RUN_NOT_FOUND"},
		{ErrAccountTestBusy, http.StatusTooManyRequests, "ACCOUNT_TEST_BUSY"},
		{ErrAccountRecoveredSchedulableFailed, http.StatusInternalServerError, "ACCOUNT_RECOVERED_SCHEDULABLE_FAILED"},
		{ErrProxyNotFound, http.StatusNotFound, "PROXY_NOT_FOUND"},
		{ErrClaudeOAuthSessionNotFound, http.StatusBadRequest, "CLAUDE_OAUTH_SESSION_NOT_FOUND"},
		{ErrClaudeOAuthInvalidState, http.StatusBadRequest, "CLAUDE_OAUTH_INVALID_STATE"},
		{ErrGeminiOAuthSessionNotFound, http.StatusBadRequest, "GEMINI_OAUTH_SESSION_NOT_FOUND"},
		{ErrGeminiOAuthInvalidState, http.StatusBadRequest, "GEMINI_OAUTH_INVALID_STATE"},
		{ErrAntigravityOAuthSessionNotFound, http.StatusBadRequest, "ANTIGRAVITY_OAUTH_SESSION_NOT_FOUND"},
		{ErrAntigravityOAuthInvalidState, http.StatusBadRequest, "ANTIGRAVITY_OAUTH_INVALID_STATE"},
		{NewOAuthCodeRejectedError(ClaudeOAuthCodeRejectedReason, "m", http.StatusBadRequest), http.StatusBadRequest, "CLAUDE_OAUTH_CODE_REJECTED"},
		{NewOAuthCodeRejectedError(GeminiOAuthCodeRejectedReason, "m", http.StatusBadRequest), http.StatusBadRequest, "GEMINI_OAUTH_CODE_REJECTED"},
		{NewOAuthCodeRejectedError(AntigravityOAuthCodeRejectedReason, "m", http.StatusBadRequest), http.StatusBadRequest, "ANTIGRAVITY_OAUTH_CODE_REJECTED"},
		{NewOAuthCodeRejectedError(OpenAIOAuthCodeRejectedReason, "m", http.StatusBadRequest), http.StatusBadRequest, "OPENAI_OAUTH_CODE_REJECTED"},
		{NewOAuthCodeRejectedError(GrokOAuthCodeRejectedReason, "m", http.StatusBadRequest), http.StatusBadRequest, "GROK_OAUTH_CODE_REJECTED"},
		{ErrAccountNotOAuth, http.StatusBadRequest, "NOT_OAUTH"},
		{ErrReauthUnsupported, http.StatusBadRequest, "REAUTH_UNSUPPORTED"},
		{ErrOAuthExchangeFailed, http.StatusBadGateway, "OAUTH_EXCHANGE_FAILED"},
		{ErrReauthPersistFailed, http.StatusInternalServerError, "REAUTH_PERSIST_FAILED"},
	}
	for _, tc := range cases {
		require.Equal(t, tc.reason, infraerrors.Reason(tc.err))
		require.Equal(t, tc.status, infraerrors.Code(tc.err), tc.reason)
	}
}
