package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestLogin2FAExpiredSessionReturnsReason(t *testing.T) {
	handler, _ := newOAuthPendingFlowTestHandlerWithDependencies(t, oauthPendingFlowTestHandlerOptions{
		settingValues: map[string]string{service.SettingKeyTotpEnabled: "true"},
		totpCache:     &oauthPendingFlowTotpCacheStub{},
		totpEncryptor: oauthPendingFlowTotpEncryptorStub{},
	})

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	body := bytes.NewBufferString(`{"temp_token":"expired-temp-token","totp_code":"123456"}`)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login/2fa", body)
	ginCtx.Request.Header.Set("Content-Type", "application/json")
	handler.Login2FA(ginCtx)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, "TOTP_LOGIN_SESSION_EXPIRED", payload["reason"])
}
