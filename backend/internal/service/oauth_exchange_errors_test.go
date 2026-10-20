//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/oauth"
	"github.com/stretchr/testify/require"
)

func TestOAuthCodeRejectedClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"oauth 400", &oauth.TokenExchangeError{StatusCode: http.StatusBadRequest}, true},
		{"oauth 400 invalid_grant", &oauth.TokenExchangeError{StatusCode: http.StatusBadRequest, Body: `{"error":"invalid_grant"}`}, true},
		{"oauth 403 invalid_grant", &oauth.TokenExchangeError{StatusCode: http.StatusForbidden, Body: `{"error":"invalid_grant"}`}, true},
		{"oauth 401 invalid_client", &oauth.TokenExchangeError{StatusCode: http.StatusUnauthorized, Body: `{"error":"invalid_client"}`}, false},
		{"oauth 403 cloudflare", &oauth.TokenExchangeError{StatusCode: http.StatusForbidden, Body: `<html>Just a moment...</html>`}, false},
		{"oauth 401 empty", &oauth.TokenExchangeError{StatusCode: http.StatusUnauthorized}, false},
		{"antigravity 400", &antigravity.TokenExchangeError{StatusCode: http.StatusBadRequest}, true},
		{"wrapped antigravity 401 invalid_grant", fmt.Errorf("token 交换失败: %w", &antigravity.TokenExchangeError{StatusCode: http.StatusUnauthorized, Body: `{"error":"invalid_grant"}`}), true},
		{"wrapped antigravity 401 invalid_client", fmt.Errorf("token 交换失败: %w", &antigravity.TokenExchangeError{StatusCode: http.StatusUnauthorized, Body: `{"error":"invalid_client"}`}), false},
		{"oauth 408", &oauth.TokenExchangeError{StatusCode: http.StatusRequestTimeout}, false},
		{"oauth 429", &oauth.TokenExchangeError{StatusCode: http.StatusTooManyRequests}, false},
		{"oauth 500", &oauth.TokenExchangeError{StatusCode: http.StatusInternalServerError}, false},
		{"antigravity 502", &antigravity.TokenExchangeError{StatusCode: http.StatusBadGateway}, false},
		{"plain error", errors.New("request failed: dial tcp: timeout"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := oauthCodeRejected(GeminiOAuthCodeRejectedReason, "msg", tc.err)
			require.Equal(t, tc.want, ok)
			if !tc.want {
				require.Nil(t, got)
				return
			}
			require.Equal(t, http.StatusBadRequest, infraerrors.Code(got))
			require.Equal(t, GeminiOAuthCodeRejectedReason, infraerrors.Reason(got))
			require.Equal(t, "msg", infraerrors.Message(got))
			require.NotEmpty(t, infraerrors.FromError(got).Metadata["upstream_status"])
			require.ErrorIs(t, got, tc.err, "原始错误应保留在 cause 链里，便于日志排查")
		})
	}
}

func TestAntigravityOAuthService_ExchangeCode_SessionAndStateReasons(t *testing.T) {
	svc := NewAntigravityOAuthService(nil)
	defer svc.Stop()

	_, err := svc.ExchangeCode(context.Background(), &AntigravityExchangeCodeInput{SessionID: "missing", State: "s", Code: "c"})
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
	require.Equal(t, "ANTIGRAVITY_OAUTH_SESSION_NOT_FOUND", infraerrors.Reason(err))
	require.Equal(t, "session 不存在或已过期", infraerrors.Message(err))

	svc.sessionStore.Set("sess", &antigravity.OAuthSession{State: "correct-state", CodeVerifier: "v", CreatedAt: time.Now()})
	for _, state := range []string{"wrong-state", "", "   "} {
		_, err = svc.ExchangeCode(context.Background(), &AntigravityExchangeCodeInput{SessionID: "sess", State: state, Code: "c"})
		require.Error(t, err)
		require.Equal(t, http.StatusBadRequest, infraerrors.Code(err), "state %q", state)
		require.Equal(t, "ANTIGRAVITY_OAUTH_INVALID_STATE", infraerrors.Reason(err), "state %q", state)
		require.Equal(t, "state 无效", infraerrors.Message(err), "state %q", state)
	}
}
