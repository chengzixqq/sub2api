//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type claudeCustomizationRepoStub struct {
	value  string
	setKey string
	setVal string
	getErr error
	setErr error
}

func (r *claudeCustomizationRepoStub) Get(context.Context, string) (*Setting, error) {
	return nil, ErrSettingNotFound
}
func (r *claudeCustomizationRepoStub) GetValue(context.Context, string) (string, error) {
	if r.getErr != nil {
		return "", r.getErr
	}
	if r.value == "" {
		return "", ErrSettingNotFound
	}
	return r.value, nil
}
func (r *claudeCustomizationRepoStub) Set(_ context.Context, key, value string) error {
	r.setKey, r.setVal = key, value
	r.value = value
	return r.setErr
}
func (r *claudeCustomizationRepoStub) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *claudeCustomizationRepoStub) SetMultiple(context.Context, map[string]string) error {
	return nil
}
func (r *claudeCustomizationRepoStub) GetAll(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *claudeCustomizationRepoStub) Delete(context.Context, string) error { return nil }

func TestClaudeCustomization_DefaultsToMagicPreset(t *testing.T) {
	svc := NewSettingService(&claudeCustomizationRepoStub{}, &config.Config{})
	got, err := svc.GetClaudeCustomizationSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, ClaudePresetMagic, got.Preset)
	require.Equal(t, ClaudeFallbackNativePassthrough, got.FallbackPolicy)
	require.False(t, got.ThinkingPrefilterEnabled)
	require.False(t, got.ThinkingSignatureRetryEnabled)
	require.False(t, got.ThinkingToolDowngradeRetryEnabled)
}

func TestSignatureRectifierDisabledWhenSettingsReadFails(t *testing.T) {
	repo := &claudeCustomizationRepoStub{getErr: errors.New("settings unavailable")}
	svc := NewSettingService(repo, &config.Config{})

	require.False(t, svc.IsSignatureRectifierEnabled(context.Background()))
}

func TestClaudeCustomization_OfficialPresetMaterializesStrictValues(t *testing.T) {
	repo := &claudeCustomizationRepoStub{}
	svc := NewSettingService(repo, &config.Config{})
	require.NoError(t, svc.SetClaudeCustomizationSettings(context.Background(), ClaudeCustomizationSettings{Preset: ClaudePresetOfficial}))
	got, err := svc.GetClaudeCustomizationSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, ClaudeFallbackStrict, got.FallbackPolicy)
	require.True(t, got.ThinkingPrefilterEnabled)
	require.Equal(t, ClaudeBetaOfficialStrict, got.BetaPolicyMode)
	require.Equal(t, SettingKeyClaudeCustomization, repo.setKey)
}

func TestClaudeCustomization_AccountOverridesHavePrecedence(t *testing.T) {
	repo := &claudeCustomizationRepoStub{}
	global := DefaultClaudeCustomizationSettings()
	global.ThinkingPrefilterEnabled = true
	raw, err := json.Marshal(global)
	require.NoError(t, err)
	repo.value = string(raw)
	svc := NewSettingService(repo, &config.Config{})
	account := &Account{Extra: map[string]any{
		AccountClaudeCustomizationExtraKey: map[string]any{
			"thinking_prefilter_enabled": false,
			"fallback_policy":            ClaudeFallbackStrict,
		},
	}}
	got, overrides, err := svc.ResolveClaudeCustomization(context.Background(), account)
	require.NoError(t, err)
	require.False(t, got.ThinkingPrefilterEnabled)
	require.Equal(t, ClaudeFallbackStrict, got.FallbackPolicy)
	require.Len(t, overrides, 2)
}

func TestClaudeCustomization_AccountOverrideStillAppliesWhenGlobalSettingsFail(t *testing.T) {
	tests := []struct {
		name    string
		global  string
		getErr  error
		wantErr string
	}{
		{
			name:    "repository error",
			getErr:  errors.New("settings unavailable"),
			wantErr: "settings unavailable",
		},
		{
			name:    "malformed json",
			global:  `{"preset":`,
			wantErr: "decode claude customization",
		},
		{
			name:    "invalid enum",
			global:  `{"preset":"invalid"}`,
			wantErr: "invalid preset",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &claudeCustomizationRepoStub{value: tt.global, getErr: tt.getErr}
			svc := NewSettingService(repo, &config.Config{})
			account := &Account{ID: 77, Extra: map[string]any{
				AccountClaudeCustomizationExtraKey: map[string]any{
					"thinking_signature_retry_enabled": false,
				},
			}}

			got, overrides, err := svc.ResolveClaudeCustomization(context.Background(), account)
			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErr)
			require.False(t, got.ThinkingSignatureRetryEnabled)
			require.Equal(t, map[string]any{"thinking_signature_retry_enabled": false}, overrides)
		})
	}
}

func TestClaudeCustomization_RequestUsesAccountOverrideWhenGlobalSettingsFail(t *testing.T) {
	repo := &claudeCustomizationRepoStub{getErr: errors.New("settings unavailable")}
	svc := NewSettingService(repo, &config.Config{})
	account := &Account{ID: 78, Extra: map[string]any{
		AccountClaudeCustomizationExtraKey: map[string]any{
			"thinking_signature_retry_enabled": false,
		},
	}}

	got := svc.ResolveClaudeCustomizationForRequest(context.Background(), nil, account)
	require.False(t, got.ThinkingSignatureRetryEnabled)
}

func TestClaudeCustomization_RequestKeepsGlobalPolicyWhenAccountOverridesAreMalformed(t *testing.T) {
	repo := &claudeCustomizationRepoStub{}
	global := DefaultClaudeCustomizationSettings()
	global.Preset = ClaudePresetCustom
	global.FallbackPolicy = ClaudeFallbackStrict
	raw, err := json.Marshal(global)
	require.NoError(t, err)
	repo.value = string(raw)

	svc := NewSettingService(repo, &config.Config{})
	account := &Account{ID: 66, Extra: map[string]any{
		AccountClaudeCustomizationExtraKey: "malformed",
	}}

	got := svc.ResolveClaudeCustomizationForRequest(context.Background(), nil, account)
	require.Equal(t, ClaudeFallbackStrict, got.FallbackPolicy)
}

func TestShouldRectifySignatureError_ReusesRequestPolicy(t *testing.T) {
	global := DefaultClaudeCustomizationSettings()
	global.Preset = ClaudePresetCustom
	global.ThinkingSignatureRetryEnabled = true
	raw, err := json.Marshal(global)
	require.NoError(t, err)
	repo := &claudeCustomizationRepoStub{value: string(raw)}
	settingService := NewSettingService(repo, &config.Config{})
	gateway := &GatewayService{settingService: settingService}
	account := &Account{ID: 79, Type: AccountTypeAPIKey}
	requestBody := []byte(`{"thinking":{"type":"enabled"},"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"prior","signature":"bad"}]}]}`)
	responseBody := []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"thinking signature is invalid"}}`)
	c, _ := gin.CreateTestContext(nil)

	// Cache the request policy as the gateway does before the first upstream call.
	require.True(t, settingService.ResolveClaudeCustomizationForRequest(context.Background(), c, account).ThinkingSignatureRetryEnabled)

	// A later settings read would disable retries, but the in-flight request must
	// keep the policy that was resolved at its entry point.
	disabled := global
	disabled.ThinkingSignatureRetryEnabled = false
	disabledRaw, err := json.Marshal(disabled)
	require.NoError(t, err)
	repo.value = string(disabledRaw)

	require.True(t, gateway.shouldRectifySignatureError(context.Background(), c, account, responseBody, requestBody, "claude-sonnet-4-5"))
}

func TestClaudeCustomization_RequestCacheIsScopedToAccount(t *testing.T) {
	repo := &claudeCustomizationRepoStub{}
	global := DefaultClaudeCustomizationSettings()
	raw, err := json.Marshal(global)
	require.NoError(t, err)
	repo.value = string(raw)
	svc := NewSettingService(repo, &config.Config{})
	c, _ := gin.CreateTestContext(nil)
	first := &Account{ID: 1, Extra: map[string]any{AccountClaudeCustomizationExtraKey: map[string]any{
		"fallback_policy": ClaudeFallbackStrict,
	}}}
	second := &Account{ID: 2, Extra: map[string]any{AccountClaudeCustomizationExtraKey: map[string]any{
		"fallback_policy": ClaudeFallbackNativePassthrough,
	}}}
	require.Equal(t, ClaudeFallbackStrict, svc.ResolveClaudeCustomizationForRequest(context.Background(), c, first).FallbackPolicy)
	require.Equal(t, ClaudeFallbackNativePassthrough, svc.ResolveClaudeCustomizationForRequest(context.Background(), c, second).FallbackPolicy)
}

func TestNormalizeClaudeCustomizationOverridesRejectsUnsafeFields(t *testing.T) {
	_, err := NormalizeClaudeCustomizationOverrides(map[string]any{"preset": ClaudePresetOfficial})
	require.Error(t, err)
	_, err = NormalizeClaudeCustomizationOverrides(map[string]any{"thinking_prefilter_enabled": "false"})
	require.Error(t, err)
	got, err := NormalizeClaudeCustomizationOverrides(map[string]any{"url_redaction_enabled": nil})
	require.NoError(t, err)
	require.Contains(t, got, "url_redaction_enabled")
}

func TestClaudeCustomization_FallbackPolicyControlsBodySanitization(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-7","fallbacks":"default","fallback_credit_token":"tok","messages":[]}`)

	got, changed := sanitizeAnthropicBodyForBetaTokensWithFallbackPolicy(
		body, claude.BetaServerSideFallback+","+claude.BetaFallbackCredit,
		ClaudeFallbackNativePassthrough, true,
	)
	require.False(t, changed)
	require.True(t, json.Valid(got))
	require.Contains(t, string(got), `"fallbacks"`)

	got, changed = sanitizeAnthropicBodyForBetaTokensWithFallbackPolicy(
		body, claude.BetaServerSideFallback+","+claude.BetaFallbackCredit,
		ClaudeFallbackNativePassthrough, false,
	)
	require.True(t, changed)
	require.NotContains(t, string(got), `"fallbacks"`)
	require.NotContains(t, string(got), `"fallback_credit_token"`)

	got, changed = sanitizeAnthropicBodyForBetaTokensWithFallbackPolicy(
		body, claude.BetaServerSideFallback,
		ClaudeFallbackStrict, true,
	)
	require.True(t, changed)
	require.NotContains(t, string(got), `"fallbacks"`)
}
