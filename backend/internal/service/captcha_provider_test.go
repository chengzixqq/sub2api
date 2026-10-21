//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestEffectiveCaptchaProvider(t *testing.T) {
	cases := []struct {
		name                       string
		turnstile, tencent, aliyun bool
		want                       string
	}{
		{name: "none enabled", want: ""},
		{name: "turnstile only", turnstile: true, want: CaptchaProviderTurnstile},
		{name: "tencent only", tencent: true, want: CaptchaProviderTencent},
		{name: "aliyun only", aliyun: true, want: CaptchaProviderAliyun},
		{name: "turnstile and tencent", turnstile: true, tencent: true, want: CaptchaProviderConflict},
		{name: "turnstile and aliyun", turnstile: true, aliyun: true, want: CaptchaProviderConflict},
		{name: "tencent and aliyun", tencent: true, aliyun: true, want: CaptchaProviderConflict},
		{name: "all three", turnstile: true, tencent: true, aliyun: true, want: CaptchaProviderConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, effectiveCaptchaProvider(tc.turnstile, tc.tencent, tc.aliyun))
			providerConfig := CaptchaProviderConfig{
				TurnstileEnabled: tc.turnstile,
				Tencent:          TencentCaptchaConfig{Enabled: tc.tencent},
				Aliyun:           AliyunCaptchaConfig{Enabled: tc.aliyun},
			}
			require.Equal(t, tc.want, providerConfig.EffectiveProvider())
		})
	}
}

func requireCaptchaProviderMismatch(t *testing.T, err error, effective string) {
	t.Helper()
	require.ErrorIs(t, err, ErrCaptchaProviderMismatch)
	require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
	require.Equal(t, "CAPTCHA_PROVIDER_MISMATCH", infraerrors.Reason(err))
	metadata := infraerrors.FromError(err).Metadata
	require.Contains(t, metadata, "captcha_provider")
	require.Equal(t, effective, metadata["captcha_provider"])
}

func TestAuthServiceVerifyCaptchaAcceptsAliyunParamOnly(t *testing.T) {
	spy := &aliyunVerifierSpy{}
	authService := newAliyunAuthServiceForTest(&config.Config{}, aliyunEnabledSettings(), spy)

	err := authService.VerifyCaptcha(context.Background(), CaptchaProof{AliyunParam: "p1"}, "127.0.0.1")

	require.NoError(t, err)
	require.Equal(t, 1, spy.called)
	require.Equal(t, "p1", spy.lastParam)
}

func TestAuthServiceVerifyCaptchaPrefersAliyunParamOverTurnstileToken(t *testing.T) {
	spy := &aliyunVerifierSpy{}
	authService := newAliyunAuthServiceForTest(&config.Config{}, aliyunEnabledSettings(), spy)

	err := authService.VerifyCaptcha(context.Background(), CaptchaProof{AliyunParam: "new", TurnstileToken: "legacy"}, "127.0.0.1")
	require.NoError(t, err)
	require.Equal(t, "new", spy.lastParam)

	// 只有空白的 aliyun_captcha_param 视为没传，回退到旧字段
	err = authService.VerifyCaptcha(context.Background(), CaptchaProof{AliyunParam: "  \t", TurnstileToken: "legacy"}, "127.0.0.1")
	require.NoError(t, err)
	require.Equal(t, "legacy", spy.lastParam)
	require.Equal(t, 2, spy.called)
}

func TestAuthServiceVerifyCaptchaRejectsDeclaredProviderMismatch(t *testing.T) {
	spy := &aliyunVerifierSpy{}
	authService := newAliyunAuthServiceForTest(&config.Config{}, aliyunEnabledSettings(), spy)

	err := authService.VerifyCaptcha(context.Background(), CaptchaProof{Provider: "turnstile", TurnstileToken: "token"}, "127.0.0.1")

	requireCaptchaProviderMismatch(t, err, CaptchaProviderAliyun)
	require.Zero(t, spy.called)
}

func TestAuthServiceVerifyCaptchaAcceptsDeclaredProviderCaseInsensitive(t *testing.T) {
	spy := &aliyunVerifierSpy{}
	authService := newAliyunAuthServiceForTest(&config.Config{}, aliyunEnabledSettings(), spy)

	err := authService.VerifyCaptcha(context.Background(), CaptchaProof{Provider: " Aliyun ", AliyunParam: "param"}, "127.0.0.1")

	require.NoError(t, err)
	require.Equal(t, 1, spy.called)
	require.Equal(t, "param", spy.lastParam)
}

func TestAuthServiceVerifyCaptchaConflictWinsOverDeclaredProvider(t *testing.T) {
	settings := aliyunEnabledSettings()
	settings[SettingKeyTurnstileEnabled] = "true"
	settings[SettingKeyTurnstileSecretKey] = "secret"
	spy := &aliyunVerifierSpy{}
	authService := newAliyunAuthServiceForTest(&config.Config{}, settings, spy)

	for _, declared := range []string{"aliyun", "turnstile", "tencent", ""} {
		err := authService.VerifyCaptcha(context.Background(), CaptchaProof{Provider: declared, AliyunParam: "param"}, "127.0.0.1")

		require.ErrorIs(t, err, ErrCaptchaProviderConflict, "declared=%q", declared)
		require.Equal(t, http.StatusServiceUnavailable, infraerrors.Code(err))
	}
	require.Zero(t, spy.called)
}

func TestAuthServiceVerifyCaptchaRejectsDeclaredProviderWhenNoneEnabled(t *testing.T) {
	spy := &aliyunVerifierSpy{}
	authService := newAliyunAuthServiceForTest(&config.Config{}, map[string]string{}, spy)

	err := authService.VerifyCaptcha(context.Background(), CaptchaProof{Provider: "aliyun", AliyunParam: "param"}, "127.0.0.1")
	requireCaptchaProviderMismatch(t, err, "")

	// 不带声明时旧行为不变：站点没开验证码直接放行
	require.NoError(t, authService.VerifyCaptcha(context.Background(), CaptchaProof{AliyunParam: "param"}, "127.0.0.1"))
	require.Zero(t, spy.called)
}

func TestAuthServiceVerifyCaptchaRejectsDeclaredProviderOnTencentSite(t *testing.T) {
	verifier := &tencentCaptchaVerifierStub{response: &TencentCaptchaVerifyResponse{CaptchaCode: 1}}
	svc := newAuthServiceForCaptchaTest(tencentCaptchaSettings(), false, nil, verifier)

	err := svc.VerifyCaptcha(context.Background(), CaptchaProof{
		Provider:       "aliyun",
		TencentTicket:  "ticket",
		TencentRandstr: "@rand",
	}, "203.0.113.10")
	requireCaptchaProviderMismatch(t, err, CaptchaProviderTencent)
	require.Zero(t, verifier.calls)

	err = svc.VerifyCaptcha(context.Background(), CaptchaProof{
		Provider:       "tencent",
		TencentTicket:  "ticket",
		TencentRandstr: "@rand",
	}, "203.0.113.10")
	require.NoError(t, err)
	require.Equal(t, 1, verifier.calls)
}

func TestAuthServiceVerifyCaptchaForRegisterChecksDeclaredProvider(t *testing.T) {
	verifier := &tencentCaptchaVerifierStub{response: &TencentCaptchaVerifyResponse{CaptchaCode: 1}}
	svc := newAuthServiceForCaptchaTest(tencentCaptchaSettings(), false, nil, verifier)

	err := svc.VerifyCaptchaForRegister(context.Background(), CaptchaProof{
		Provider:       "aliyun",
		TencentTicket:  "ticket",
		TencentRandstr: "@rand",
	}, "203.0.113.10", "")

	requireCaptchaProviderMismatch(t, err, CaptchaProviderTencent)
	require.Zero(t, verifier.calls)
}

func TestAuthServiceVerifyActionCaptchaPrefersAliyunParam(t *testing.T) {
	spy := &aliyunVerifierSpy{}
	authService := newAliyunAuthServiceForTest(&config.Config{}, aliyunEnabledSettings(), spy)

	err := authService.VerifyActionCaptchaIfEnabled(context.Background(), CaptchaProof{AliyunParam: "action-param"}, "127.0.0.1")
	require.NoError(t, err)
	require.Equal(t, 1, spy.called)
	require.Equal(t, "action-param", spy.lastParam)

	err = authService.VerifyActionCaptchaIfEnabled(context.Background(), CaptchaProof{AliyunParam: "new", TurnstileToken: "legacy"}, "127.0.0.1")
	require.NoError(t, err)
	require.Equal(t, "new", spy.lastParam)
}

func TestAuthServiceVerifyActionCaptchaRejectsDeclaredProviderMismatch(t *testing.T) {
	verifier := &tencentCaptchaVerifierStub{response: &TencentCaptchaVerifyResponse{CaptchaCode: 1}}
	svc := newAuthServiceForCaptchaTest(tencentCaptchaSettings(), false, nil, verifier)

	err := svc.VerifyActionCaptchaIfEnabled(context.Background(), CaptchaProof{
		Provider:       "aliyun",
		TencentTicket:  "ticket",
		TencentRandstr: "@rand",
	}, "203.0.113.10")

	requireCaptchaProviderMismatch(t, err, CaptchaProviderTencent)
	require.Zero(t, verifier.calls)
}

func TestAuthServiceVerifyActionCaptchaConflictWinsOverDeclaredProvider(t *testing.T) {
	settings := aliyunEnabledSettings()
	settings[SettingKeyTurnstileEnabled] = "true"
	settings[SettingKeyTurnstileSecretKey] = "secret"
	spy := &aliyunVerifierSpy{}
	authService := newAliyunAuthServiceForTest(&config.Config{}, settings, spy)

	err := authService.VerifyActionCaptchaIfEnabled(context.Background(), CaptchaProof{Provider: "turnstile", AliyunParam: "param"}, "127.0.0.1")

	require.ErrorIs(t, err, ErrCaptchaProviderConflict)
	require.Zero(t, spy.called)
}

func TestAuthServiceVerifyActionCaptchaTurnstileOnlyIgnoresMatchingDeclaration(t *testing.T) {
	spy := &aliyunVerifierSpy{}
	authService := newAliyunAuthServiceForTest(&config.Config{}, map[string]string{
		SettingKeyTurnstileEnabled:   "true",
		SettingKeyTurnstileSecretKey: "secret",
	}, spy)

	// 动作入口仍不扩大 Turnstile 覆盖：声明一致且不带证明时照常放行
	require.NoError(t, authService.VerifyActionCaptchaIfEnabled(context.Background(), CaptchaProof{Provider: "turnstile"}, "127.0.0.1"))

	err := authService.VerifyActionCaptchaIfEnabled(context.Background(), CaptchaProof{Provider: "aliyun"}, "127.0.0.1")
	requireCaptchaProviderMismatch(t, err, CaptchaProviderTurnstile)
	require.Zero(t, spy.called)
}
