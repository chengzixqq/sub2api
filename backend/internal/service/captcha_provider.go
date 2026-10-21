package service

import "strings"

// Captcha provider names published as captcha_provider by GET /api/v1/settings/public
// and accepted as the optional captcha_provider declaration on captcha-protected
// auth requests.
const (
	CaptchaProviderTurnstile = "turnstile"
	CaptchaProviderTencent   = "tencent"
	CaptchaProviderAliyun    = "aliyun"
	// CaptchaProviderConflict means several providers are enabled at once;
	// captcha-protected requests fail with CAPTCHA_PROVIDER_CONFLICT until an
	// admin keeps only one.
	CaptchaProviderConflict = "conflict"
)

// effectiveCaptchaProvider names the provider VerifyCaptcha dispatches to for
// the given enabled switches: "" when none is enabled, CaptchaProviderConflict
// when more than one is.
func effectiveCaptchaProvider(turnstileEnabled, tencentEnabled, aliyunEnabled bool) string {
	switch {
	case captchaProvidersConflict(turnstileEnabled, tencentEnabled, aliyunEnabled):
		return CaptchaProviderConflict
	case tencentEnabled:
		return CaptchaProviderTencent
	case aliyunEnabled:
		return CaptchaProviderAliyun
	case turnstileEnabled:
		return CaptchaProviderTurnstile
	default:
		return ""
	}
}

// EffectiveProvider reports the provider in force for this configuration.
func (c CaptchaProviderConfig) EffectiveProvider() string {
	return effectiveCaptchaProvider(c.TurnstileEnabled, c.Tencent.Enabled, c.Aliyun.Enabled)
}

// aliyunVerifyParam prefers the dedicated aliyun_captcha_param and falls back
// to the legacy turnstile_token field Web clients still use for Aliyun.
func (p CaptchaProof) aliyunVerifyParam() string {
	if strings.TrimSpace(p.AliyunParam) != "" {
		return p.AliyunParam
	}
	return p.TurnstileToken
}

// checkDeclaredCaptchaProvider rejects a proof obtained from a provider other
// than the one in force (the site switched providers after the client fetched
// public settings). An empty declaration skips the check, so clients that do
// not declare keep the previous behavior.
func checkDeclaredCaptchaProvider(declared, effective string) error {
	declared = strings.ToLower(strings.TrimSpace(declared))
	if declared == "" || declared == effective {
		return nil
	}
	return ErrCaptchaProviderMismatch.WithMetadata(map[string]string{"captcha_provider": effective})
}
