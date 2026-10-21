//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAuthRequestsBindTencentCaptchaProof(t *testing.T) {
	const payload = `{"email":"user@example.com","password":"secret-123","tencent_captcha_ticket":"ticket-value","tencent_captcha_randstr":"@rand-value"}`

	tests := []struct {
		name   string
		decode func([]byte) service.CaptchaProof
	}{
		{
			name: "登录",
			decode: func(raw []byte) service.CaptchaProof {
				var req LoginRequest
				require.NoError(t, json.Unmarshal(raw, &req))
				return captchaProof(req.TurnstileToken, req.TencentCaptchaTicket, req.TencentCaptchaRandstr)
			},
		},
		{
			name: "注册",
			decode: func(raw []byte) service.CaptchaProof {
				var req RegisterRequest
				require.NoError(t, json.Unmarshal(raw, &req))
				return captchaProof(req.TurnstileToken, req.TencentCaptchaTicket, req.TencentCaptchaRandstr)
			},
		},
		{
			name: "发送邮箱验证码",
			decode: func(raw []byte) service.CaptchaProof {
				var req SendVerifyCodeRequest
				require.NoError(t, json.Unmarshal(raw, &req))
				return captchaProof(req.TurnstileToken, req.TencentCaptchaTicket, req.TencentCaptchaRandstr)
			},
		},
		{
			name: "忘记密码",
			decode: func(raw []byte) service.CaptchaProof {
				var req ForgotPasswordRequest
				require.NoError(t, json.Unmarshal(raw, &req))
				return captchaProof(req.TurnstileToken, req.TencentCaptchaTicket, req.TencentCaptchaRandstr)
			},
		},
		{
			name: "OAuth启动",
			decode: func(raw []byte) service.CaptchaProof {
				var req oauthStartCaptchaRequest
				require.NoError(t, json.Unmarshal(raw, &req))
				return captchaProof(req.TurnstileToken, req.TencentCaptchaTicket, req.TencentCaptchaRandstr)
			},
		},
		{
			name: "Passkey登录",
			decode: func(raw []byte) service.CaptchaProof {
				var req passkeyBeginLoginRequest
				require.NoError(t, json.Unmarshal(raw, &req))
				return captchaProof(req.TurnstileToken, req.TencentCaptchaTicket, req.TencentCaptchaRandstr)
			},
		},
		{
			name: "OAuth待处理账号发送邮箱验证码",
			decode: func(raw []byte) service.CaptchaProof {
				var req sendPendingOAuthVerifyCodeRequest
				require.NoError(t, json.Unmarshal(raw, &req))
				return captchaProof(req.TurnstileToken, req.TencentCaptchaTicket, req.TencentCaptchaRandstr)
			},
		},
		{
			name: "OAuth待处理账号创建",
			decode: func(raw []byte) service.CaptchaProof {
				var req createPendingOAuthAccountRequest
				require.NoError(t, json.Unmarshal(raw, &req))
				return captchaProof(req.TurnstileToken, req.TencentCaptchaTicket, req.TencentCaptchaRandstr)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			proof := test.decode([]byte(payload))
			require.Equal(t, "ticket-value", proof.TencentTicket)
			require.Equal(t, "@rand-value", proof.TencentRandstr)
		})
	}
}

// Native apps may declare the provider they verified against and send the
// Aliyun captchaVerifyParam in its own field; Web keeps using turnstile_token.
func TestAuthRequestsBindNativeCaptchaDeclaration(t *testing.T) {
	const payload = `{"email":"user@example.com","password":"secret-123","captcha_provider":"aliyun","aliyun_captcha_param":"param-value","turnstile_token":"legacy"}`

	tests := []struct {
		name   string
		decode func([]byte) service.CaptchaProof
	}{
		{
			name: "登录",
			decode: func(raw []byte) service.CaptchaProof {
				var req LoginRequest
				require.NoError(t, json.Unmarshal(raw, &req))
				return declaredCaptchaProof(captchaProof(req.TurnstileToken, req.TencentCaptchaTicket, req.TencentCaptchaRandstr), req.CaptchaProvider, req.AliyunCaptchaParam)
			},
		},
		{
			name: "注册",
			decode: func(raw []byte) service.CaptchaProof {
				var req RegisterRequest
				require.NoError(t, json.Unmarshal(raw, &req))
				return declaredCaptchaProof(captchaProof(req.TurnstileToken, req.TencentCaptchaTicket, req.TencentCaptchaRandstr), req.CaptchaProvider, req.AliyunCaptchaParam)
			},
		},
		{
			name: "发送邮箱验证码",
			decode: func(raw []byte) service.CaptchaProof {
				var req SendVerifyCodeRequest
				require.NoError(t, json.Unmarshal(raw, &req))
				return declaredCaptchaProof(captchaProof(req.TurnstileToken, req.TencentCaptchaTicket, req.TencentCaptchaRandstr), req.CaptchaProvider, req.AliyunCaptchaParam)
			},
		},
		{
			name: "忘记密码",
			decode: func(raw []byte) service.CaptchaProof {
				var req ForgotPasswordRequest
				require.NoError(t, json.Unmarshal(raw, &req))
				return declaredCaptchaProof(captchaProof(req.TurnstileToken, req.TencentCaptchaTicket, req.TencentCaptchaRandstr), req.CaptchaProvider, req.AliyunCaptchaParam)
			},
		},
		{
			name: "Passkey登录",
			decode: func(raw []byte) service.CaptchaProof {
				var req passkeyBeginLoginRequest
				require.NoError(t, json.Unmarshal(raw, &req))
				return declaredCaptchaProof(captchaProof(req.TurnstileToken, req.TencentCaptchaTicket, req.TencentCaptchaRandstr), req.CaptchaProvider, req.AliyunCaptchaParam)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			proof := test.decode([]byte(payload))
			require.Equal(t, "aliyun", proof.Provider)
			require.Equal(t, "param-value", proof.AliyunParam)
			require.Equal(t, "legacy", proof.TurnstileToken)
		})
	}
}

type handlerAliyunVerifierSpy struct {
	calls     int
	lastParam string
}

func (s *handlerAliyunVerifierSpy) VerifyCaptcha(_ context.Context, _ service.AliyunCaptchaCredentials, param string) (*service.AliyunCaptchaVerifyResult, error) {
	s.calls++
	s.lastParam = param
	return &service.AliyunCaptchaVerifyResult{VerifyResult: true}, nil
}

func newAliyunProtectedAuthHandler(t *testing.T) (*AuthHandler, *handlerAliyunVerifierSpy) {
	t.Helper()
	cfg := &config.Config{}
	settings := service.NewSettingService(&passkeySwitchSettingRepo{values: map[string]string{
		service.SettingKeyAliyunCaptchaEnabled:         "true",
		service.SettingKeyAliyunCaptchaAccessKeyID:     "ak-id",
		service.SettingKeyAliyunCaptchaAccessKeySecret: "ak-secret",
		service.SettingKeyAliyunCaptchaSceneID:         "scene-1",
	}}, cfg)
	spy := &handlerAliyunVerifierSpy{}
	authService := service.NewAuthService(nil, nil, nil, nil, cfg, settings, nil, nil, nil, nil, nil, nil, nil)
	authService.SetAliyunCaptchaService(service.NewAliyunCaptchaService(settings, spy))
	return NewAuthHandler(cfg, authService, nil, settings, nil, nil, nil, nil, nil), spy
}

func newAuthJSONContext(path, body string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, recorder
}

func TestAuthLoginRejectsDeclaredCaptchaProviderMismatch(t *testing.T) {
	handler, spy := newAliyunProtectedAuthHandler(t)
	c, recorder := newAuthJSONContext("/api/v1/auth/login",
		`{"email":"user@example.com","password":"secret-123","captcha_provider":"turnstile","aliyun_captcha_param":"param-value"}`)

	handler.Login(c)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	var resp struct {
		Reason   string            `json:"reason"`
		Metadata map[string]string `json:"metadata"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
	require.Equal(t, "CAPTCHA_PROVIDER_MISMATCH", resp.Reason)
	require.Equal(t, "aliyun", resp.Metadata["captcha_provider"])
	require.Zero(t, spy.calls)
}

func TestAuthSendVerifyCodeVerifiesAliyunCaptchaParam(t *testing.T) {
	handler, spy := newAliyunProtectedAuthHandler(t)
	c, recorder := newAuthJSONContext("/api/v1/auth/send-verify-code",
		`{"email":"user@example.com","captcha_provider":"aliyun","aliyun_captcha_param":"param-value"}`)

	handler.SendVerifyCode(c)

	// 验证码通过后才走到注册开关（测试站点未开放注册）
	require.Equal(t, 1, spy.calls)
	require.Equal(t, "param-value", spy.lastParam)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "REGISTRATION_DISABLED")
}
