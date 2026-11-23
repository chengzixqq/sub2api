//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestClaudeCustomizationCacheControlLimitMode_PresetDefaults(t *testing.T) {
	cfg := DefaultClaudeCustomizationSettings()
	require.Equal(t, ClaudeCacheControlLimitTrim, cfg.CacheControlLimitMode,
		"the default (magic) preset must keep trimming")
	require.NoError(t, applyClaudePreset(&cfg, ClaudePresetOfficial))
	require.Equal(t, ClaudeCacheControlLimitTrim, cfg.CacheControlLimitMode,
		"applying the official preset to defaults preserves trim")

	// No stored setting at all: the built-in default applies.
	svc := NewSettingService(&claudeCustomizationRepoStub{}, &config.Config{})
	got, err := svc.GetClaudeCustomizationSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, ClaudeCacheControlLimitTrim, got.CacheControlLimitMode)

	for _, tc := range []struct {
		preset string
		want   string
	}{
		{ClaudePresetMagic, ClaudeCacheControlLimitTrim},
		{ClaudePresetOfficial, ClaudeCacheControlLimitTrim},
	} {
		repo := &claudeCustomizationRepoStub{}
		svc := NewSettingService(repo, &config.Config{})
		require.NoError(t, svc.SetClaudeCustomizationSettings(context.Background(), ClaudeCustomizationSettings{Preset: tc.preset}))
		require.Equal(t, tc.want, gjson.Get(repo.setVal, "cache_control_limit_mode").String(),
			"preset %s must preserve the historical default", tc.preset)
		got, err := svc.GetClaudeCustomizationSettings(context.Background())
		require.NoError(t, err)
		require.Equal(t, tc.want, got.CacheControlLimitMode, "preset %s", tc.preset)
	}
}

// Global JSON written before the field existed must keep the production
// behavior (trim), whatever preset it stores.
func TestClaudeCustomizationCacheControlLimitMode_LegacyGlobalJSONIsTrim(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"custom", `{"preset":"custom","fallback_policy":"strict","thinking_prefilter_enabled":true,"thinking_signature_retry_enabled":false,"thinking_tool_downgrade_retry_enabled":false,"beta_policy_mode":"official_strict","unknown_beta_action":"filter","fingerprint_unification":true,"metadata_passthrough":false,"url_redaction_enabled":true}`},
		{"magic", `{"preset":"magic","fallback_policy":"native_passthrough","beta_policy_mode":"capability_aware","unknown_beta_action":"pass_on_native_only"}`},
		{"official", `{"preset":"official","fallback_policy":"strict","beta_policy_mode":"official_strict","unknown_beta_action":"filter"}`},
		{"explicit_null", `{"preset":"official","fallback_policy":"strict","beta_policy_mode":"official_strict","unknown_beta_action":"filter","cache_control_limit_mode":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewSettingService(&claudeCustomizationRepoStub{value: tc.raw}, &config.Config{})
			got, err := svc.GetClaudeCustomizationSettings(context.Background())
			require.NoError(t, err)
			require.Equal(t, ClaudeCacheControlLimitTrim, got.CacheControlLimitMode)

			// The request path and the account-scoped read see the same value.
			account := &Account{ID: 1, Extra: map[string]any{}}
			effective, _, err := svc.ResolveClaudeCustomization(context.Background(), account)
			require.NoError(t, err)
			require.Equal(t, ClaudeCacheControlLimitTrim, effective.CacheControlLimitMode)
			require.Equal(t, ClaudeCacheControlLimitTrim, cacheControlLimitMode(svc.ResolveClaudeCustomizationForRequest(context.Background(), nil, account)))
		})
	}

	// Other legacy fields are not affected by the new default.
	svc := NewSettingService(&claudeCustomizationRepoStub{value: `{"preset":"custom","fallback_policy":"strict","beta_policy_mode":"official_strict","unknown_beta_action":"filter","metadata_passthrough":false}`}, &config.Config{})
	got, err := svc.GetClaudeCustomizationSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, ClaudeFallbackStrict, got.FallbackPolicy)
	require.Equal(t, ClaudeBetaOfficialStrict, got.BetaPolicyMode)
	require.False(t, got.MetadataPassthrough)
}

func TestClaudeCustomizationCacheControlLimitMode_RejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"drop", "strict", "pass", "passthrough-all"} {
		cfg := DefaultClaudeCustomizationSettings()
		cfg.Preset = ClaudePresetCustom
		cfg.CacheControlLimitMode = value
		require.Error(t, normalizeClaudeCustomizationSettings(&cfg), value)

		repo := &claudeCustomizationRepoStub{}
		svc := NewSettingService(repo, &config.Config{})
		require.Error(t, svc.SetClaudeCustomizationSettings(context.Background(), cfg), value)
		require.Empty(t, repo.setVal, "an invalid mode must not be persisted")
	}

	// A corrupted field inherits its default while valid fields survive.
	svc := NewSettingService(&claudeCustomizationRepoStub{value: `{"preset":"custom","fallback_policy":"strict","beta_policy_mode":"official_strict","unknown_beta_action":"filter","cache_control_limit_mode":"drop"}`}, &config.Config{})
	got, err := svc.GetClaudeCustomizationSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, ClaudeCacheControlLimitTrim, got.CacheControlLimitMode)
	require.Equal(t, ClaudeFallbackStrict, got.FallbackPolicy)

	// Case and surrounding spaces are normalized like the other enum fields.
	cfg := DefaultClaudeCustomizationSettings()
	cfg.Preset = ClaudePresetCustom
	cfg.CacheControlLimitMode = "  PassThrough "
	require.NoError(t, normalizeClaudeCustomizationSettings(&cfg))
	require.Equal(t, ClaudeCacheControlLimitPassthrough, cfg.CacheControlLimitMode)

	for _, value := range []any{"drop", "", 1, true, map[string]any{}} {
		_, err := NormalizeClaudeCustomizationOverrides(map[string]any{"cache_control_limit_mode": value})
		require.Error(t, err, "override %v must be rejected", value)
	}
	normalized, err := NormalizeClaudeCustomizationOverrides(map[string]any{"cache_control_limit_mode": " Passthrough "})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"cache_control_limit_mode": ClaudeCacheControlLimitPassthrough}, normalized)
}

func TestClaudeCustomizationCacheControlLimitMode_AccountOverrideHasPrecedence(t *testing.T) {
	ctx := context.Background()
	globalJSON := func(mode string) string {
		cfg := DefaultClaudeCustomizationSettings()
		cfg.Preset = ClaudePresetCustom
		cfg.CacheControlLimitMode = mode
		raw, err := json.Marshal(cfg)
		require.NoError(t, err)
		return string(raw)
	}

	for _, tc := range []struct {
		name      string
		global    string
		overrides any
		want      string
	}{
		{"override_passthrough_over_global_trim", ClaudeCacheControlLimitTrim, map[string]any{"cache_control_limit_mode": "passthrough"}, ClaudeCacheControlLimitPassthrough},
		{"override_trim_over_global_passthrough", ClaudeCacheControlLimitPassthrough, map[string]any{"cache_control_limit_mode": "trim"}, ClaudeCacheControlLimitTrim},
		{"missing_field_inherits_trim", ClaudeCacheControlLimitTrim, map[string]any{"fallback_policy": "strict"}, ClaudeCacheControlLimitTrim},
		{"missing_field_inherits_passthrough", ClaudeCacheControlLimitPassthrough, map[string]any{}, ClaudeCacheControlLimitPassthrough},
		{"null_field_inherits", ClaudeCacheControlLimitPassthrough, map[string]any{"cache_control_limit_mode": nil}, ClaudeCacheControlLimitPassthrough},
		{"missing_overrides_inherit", ClaudeCacheControlLimitPassthrough, nil, ClaudeCacheControlLimitPassthrough},
		{"invalid_stored_override_falls_back_to_global", ClaudeCacheControlLimitPassthrough, map[string]any{"cache_control_limit_mode": "drop"}, ClaudeCacheControlLimitPassthrough},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewSettingService(&claudeCustomizationRepoStub{value: globalJSON(tc.global)}, &config.Config{})
			account := &Account{ID: 7, Type: AccountTypeAPIKey, Extra: map[string]any{}}
			if tc.overrides != nil {
				account.Extra[AccountClaudeCustomizationExtraKey] = tc.overrides
			}
			effective, _, err := svc.ResolveClaudeCustomization(ctx, account)
			require.NoError(t, err)
			require.Equal(t, tc.want, effective.CacheControlLimitMode)
			require.Equal(t, tc.want, svc.ResolveClaudeCustomizationForRequest(ctx, nil, account).CacheControlLimitMode)
		})
	}
}

// An admin client built before this field existed sends the full policy
// without cache_control_limit_mode. Saving it must not flip a stored
// passthrough back to trim.
func TestClaudeCustomizationCacheControlLimitMode_SaveWithoutFieldKeepsStoredMode(t *testing.T) {
	ctx := context.Background()
	stored := DefaultClaudeCustomizationSettings()
	stored.Preset = ClaudePresetCustom
	stored.CacheControlLimitMode = ClaudeCacheControlLimitPassthrough
	raw, err := json.Marshal(stored)
	require.NoError(t, err)
	repo := &claudeCustomizationRepoStub{value: string(raw)}
	svc := NewSettingService(repo, &config.Config{})

	update := stored
	update.CacheControlLimitMode = ""
	update.URLRedactionEnabled = false
	require.NoError(t, svc.SetClaudeCustomizationSettings(ctx, update))
	got, err := svc.GetClaudeCustomizationSettings(ctx)
	require.NoError(t, err)
	require.Equal(t, ClaudeCacheControlLimitPassthrough, got.CacheControlLimitMode)
	require.False(t, got.URLRedactionEnabled)

	// Without any stored value the omitted field becomes trim.
	empty := &claudeCustomizationRepoStub{}
	svc = NewSettingService(empty, &config.Config{})
	update = DefaultClaudeCustomizationSettings()
	update.Preset = ClaudePresetCustom
	update.CacheControlLimitMode = ""
	require.NoError(t, svc.SetClaudeCustomizationSettings(ctx, update))
	require.Equal(t, ClaudeCacheControlLimitTrim, gjson.Get(empty.setVal, "cache_control_limit_mode").String())

	// Neither built-in preset changes an independently selected mode.
	update.CacheControlLimitMode = ClaudeCacheControlLimitPassthrough
	require.NoError(t, svc.SetClaudeCustomizationSettings(ctx, update))
	require.NoError(t, svc.SetClaudeCustomizationSettings(ctx, ClaudeCustomizationSettings{Preset: ClaudePresetOfficial}))
	require.Equal(t, ClaudeCacheControlLimitPassthrough, gjson.Get(empty.setVal, "cache_control_limit_mode").String())
	require.NoError(t, svc.SetClaudeCustomizationSettings(ctx, ClaudeCustomizationSettings{Preset: ClaudePresetMagic}))
	require.Equal(t, ClaudeCacheControlLimitPassthrough, gjson.Get(empty.setVal, "cache_control_limit_mode").String())
}
