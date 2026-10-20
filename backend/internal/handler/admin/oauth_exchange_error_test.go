package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type oauthExchangeErrorBody struct {
	Code     int               `json:"code"`
	Message  string            `json:"message"`
	Reason   string            `json:"reason"`
	Metadata map[string]string `json:"metadata"`
}

func decodeOAuthExchangeError(t *testing.T, rec *httptest.ResponseRecorder) oauthExchangeErrorBody {
	t.Helper()
	var body oauthExchangeErrorBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	return body
}

func TestRespondOAuthExchangeError_ReasonAndLegacyBranches(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("reason keeps prefix and uses clean message", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		appErr := infraerrors.New(http.StatusBadRequest, "GEMINI_OAUTH_CODE_REJECTED", "failed to exchange code: token exchange failed: status 400, body: x").
			WithMetadata(map[string]string{"upstream_status": "400"})

		respondOAuthExchangeError(c, "Failed to exchange code: ", fmt.Errorf("wrapped: %w", appErr))

		require.Equal(t, http.StatusBadRequest, rec.Code)
		body := decodeOAuthExchangeError(t, rec)
		require.Equal(t, http.StatusBadRequest, body.Code)
		require.Equal(t, "GEMINI_OAUTH_CODE_REJECTED", body.Reason)
		require.Equal(t, "Failed to exchange code: failed to exchange code: token exchange failed: status 400, body: x", body.Message)
		require.Equal(t, map[string]string{"upstream_status": "400"}, body.Metadata)
	})

	t.Run("reason with non-400 status stays 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)

		respondOAuthExchangeError(c, "Token 交换失败: ", infraerrors.New(http.StatusBadGateway, "SOME_REASON", "upstream down"))

		require.Equal(t, http.StatusBadRequest, rec.Code)
		body := decodeOAuthExchangeError(t, rec)
		require.Equal(t, "SOME_REASON", body.Reason)
		require.Equal(t, "Token 交换失败: upstream down", body.Message)
	})

	t.Run("plain error keeps legacy text", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)

		respondOAuthExchangeError(c, "Token 交换失败: ", errors.New("token 交换失败: boom"))

		require.Equal(t, http.StatusBadRequest, rec.Code)
		body := decodeOAuthExchangeError(t, rec)
		require.Equal(t, http.StatusBadRequest, body.Code)
		require.Empty(t, body.Reason)
		require.Nil(t, body.Metadata)
		require.Equal(t, "Token 交换失败: token 交换失败: boom", body.Message)
	})
}

func TestOAuthExchangeHandlers_SessionNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)

	claudeSvc := service.NewOAuthService(nil, nil)
	t.Cleanup(claudeSvc.Stop)
	geminiSvc := service.NewGeminiOAuthService(nil, nil, nil, nil, &config.Config{})
	t.Cleanup(geminiSvc.Stop)
	antigravitySvc := service.NewAntigravityOAuthService(nil)
	t.Cleanup(antigravitySvc.Stop)

	claudeHandler := NewOAuthHandler(claudeSvc)
	router := gin.New()
	router.POST("/accounts/exchange-code", claudeHandler.ExchangeCode)
	router.POST("/accounts/exchange-setup-token-code", claudeHandler.ExchangeSetupTokenCode)
	router.POST("/gemini/oauth/exchange-code", NewGeminiOAuthHandler(geminiSvc).ExchangeCode)
	router.POST("/antigravity/oauth/exchange-code", NewAntigravityOAuthHandler(antigravitySvc).ExchangeCode)

	cases := []struct {
		path        string
		wantReason  string
		wantMessage string
	}{
		{"/accounts/exchange-code", "CLAUDE_OAUTH_SESSION_NOT_FOUND", "session not found or expired"},
		{"/accounts/exchange-setup-token-code", "CLAUDE_OAUTH_SESSION_NOT_FOUND", "session not found or expired"},
		{"/gemini/oauth/exchange-code", "GEMINI_OAUTH_SESSION_NOT_FOUND", "Failed to exchange code: session not found or expired"},
		{"/antigravity/oauth/exchange-code", "ANTIGRAVITY_OAUTH_SESSION_NOT_FOUND", "Token 交换失败: session 不存在或已过期"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			payload := []byte(`{"session_id":"missing-session","state":"some-state","code":"some-code"}`)
			req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			body := decodeOAuthExchangeError(t, rec)
			require.Equal(t, http.StatusBadRequest, body.Code)
			require.Equal(t, tc.wantReason, body.Reason)
			require.Equal(t, tc.wantMessage, body.Message)
		})
	}
}
