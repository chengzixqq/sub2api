//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

var legacyClaudeFields = []struct {
	key                     string
	magic, official, custom any
}{
	{"fallback_policy", "native_passthrough", "strict", "fable_native_passthrough"},
	{"beta_policy_mode", "capability_aware", "official_strict", "client_passthrough"},
	{"unknown_beta_action", "pass_on_native_only", "filter", "pass"},
	{"thinking_prefilter_enabled", false, true, true},
	{"thinking_signature_retry_enabled", false, false, true},
	{"thinking_tool_downgrade_retry_enabled", false, false, true},
	{"fingerprint_unification", true, true, false},
	{"metadata_passthrough", true, false, false},
	{"url_redaction_enabled", true, true, false},
}

var alignmentFields = []struct {
	key, initial, official string
}{
	{"context_management_missing_beta", "strip", "reject"},
	{"request_id_mode", "passthrough", "fill"},
	{"cache_control_limit_mode", "trim", "reject"},
	{"unknown_beta_header", "passthrough", "reject"},
	{"models_catalog", "synthetic", "official"},
	{"upstream_header_passthrough", "off", "on"},
	{"thinking_cache_control", "strip", "reject"},
	{"bedrock_cc_context_management", "strip", "passthrough"},
}

func policyJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return raw
}

func decodePolicy(t *testing.T, values map[string]any) ClaudeCustomizationSettings {
	t.Helper()
	var cfg ClaudeCustomizationSettings
	require.NoError(t, json.Unmarshal(policyJSON(t, values), &cfg))
	return cfg
}

func policyMap(t *testing.T, cfg ClaudeCustomizationSettings) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(policyJSON(t, cfg), &out))
	return out
}

func legacyPolicy(preset string) map[string]any {
	fields := map[string]any{"preset": preset}
	for _, f := range legacyClaudeFields {
		fields[f.key] = f.custom
	}
	return fields
}

func TestClaudeCustomizationFields_ExistingFieldsUnchanged(t *testing.T) {
	ctx := context.Background()
	for _, preset := range []string{"magic", "official", "custom"} {
		t.Run(preset, func(t *testing.T) {
			input := legacyPolicy(preset)
			cfg := decodePolicy(t, input)
			repo := &claudeCustomizationRepoStub{value: string(policyJSON(t, input))}
			svc := NewSettingService(repo, &config.Config{})
			got, err := svc.GetClaudeCustomizationSettings(ctx)
			require.NoError(t, err)
			read := policyMap(t, got)
			require.NoError(t, svc.SetClaudeCustomizationSettings(ctx, cfg))
			var stored map[string]any
			require.NoError(t, json.Unmarshal([]byte(repo.setVal), &stored))
			require.NoError(t, applyClaudePreset(&cfg, preset))
			applied := policyMap(t, cfg)

			for _, f := range legacyClaudeFields {
				want := map[string]any{"magic": f.magic, "official": f.official, "custom": f.custom}[preset]
				require.Equal(t, want, read[f.key], "get %s", f.key)
				require.Equal(t, want, stored[f.key], "set %s", f.key)
				require.Equal(t, want, applied[f.key], "apply preset %s", f.key)
				override := f.custom
				if want == override {
					override = f.magic
				}
				input := map[string]any{f.key: override}
				normalized, err := NormalizeClaudeCustomizationOverrides(input)
				require.NoError(t, err)
				require.Equal(t, input, normalized)
				account := &Account{Type: AccountTypeAPIKey, Extra: map[string]any{AccountClaudeCustomizationExtraKey: normalized}}
				effective, _, err := svc.ResolveClaudeCustomization(ctx, account)
				require.NoError(t, err)
				expected := policyMap(t, cfg)
				expected[f.key] = override
				require.Equal(t, expected, policyMap(t, effective))
				_, err = NormalizeClaudeCustomizationOverrides(map[string]any{f.key: 42})
				require.Error(t, err, f.key)
				nulls, err := NormalizeClaudeCustomizationOverrides(map[string]any{f.key: nil})
				require.NoError(t, err)
				require.Equal(t, map[string]any{f.key: nil}, nulls)
			}
		})
	}
}

func TestClaudeCustomizationFields_AlignmentDefaultsAreProduction(t *testing.T) {
	cfg := policyMap(t, DefaultClaudeCustomizationSettings())
	for _, f := range alignmentFields {
		require.Equal(t, f.initial, cfg[f.key], f.key)
	}
}

func TestClaudeCustomizationFields_AlignmentNotPresetManaged(t *testing.T) {
	for _, preset := range []string{"magic", "official", "custom"} {
		input := legacyPolicy("custom")
		for _, f := range alignmentFields {
			input[f.key] = f.official
		}
		cfg := decodePolicy(t, input)
		require.NoError(t, applyClaudePreset(&cfg, preset))
		got := policyMap(t, cfg)
		for _, f := range alignmentFields {
			require.Equal(t, f.official, got[f.key], "%s/%s", preset, f.key)
		}
	}
}

func TestClaudeCustomizationFields_LegacyOfficialJSONKeepsDefaults(t *testing.T) {
	input := legacyPolicy("official")
	for _, null := range []bool{false, true} {
		if null {
			for _, f := range alignmentFields {
				input[f.key] = nil
			}
		}
		svc := NewSettingService(&claudeCustomizationRepoStub{value: string(policyJSON(t, input))}, &config.Config{})
		cfg, err := svc.GetClaudeCustomizationSettings(context.Background())
		require.NoError(t, err)
		got := policyMap(t, cfg)
		for _, f := range alignmentFields {
			require.Equal(t, f.initial, got[f.key], f.key)
		}
	}
}

func TestClaudeCustomizationFields_SaveOtherFieldKeepsAlignment(t *testing.T) {
	ctx := context.Background()
	svc := NewSettingService(&claudeCustomizationRepoStub{}, &config.Config{})
	input := legacyPolicy("custom")
	for _, f := range alignmentFields {
		input[f.key] = f.official
	}
	require.NoError(t, svc.SetClaudeCustomizationSettings(ctx, decodePolicy(t, input)))
	legacy := legacyPolicy("custom")
	legacy["fallback_policy"] = "strict"
	require.NoError(t, svc.SetClaudeCustomizationSettings(ctx, decodePolicy(t, legacy)))
	cfg, err := svc.GetClaudeCustomizationSettings(ctx)
	require.NoError(t, err)
	got := policyMap(t, cfg)
	require.Equal(t, "strict", got["fallback_policy"])
	for _, f := range alignmentFields {
		require.Equal(t, f.official, got[f.key], f.key)
	}

	// One supplied choice replaces exactly that field.
	legacy["unknown_beta_header"] = "  PaSsThrough  "
	require.NoError(t, svc.SetClaudeCustomizationSettings(ctx, decodePolicy(t, legacy)))
	cfg, err = svc.GetClaudeCustomizationSettings(ctx)
	require.NoError(t, err)
	got["unknown_beta_header"] = "passthrough"
	require.Equal(t, got, policyMap(t, cfg))
}

func TestOfficialCatalogLegacyPresetAndPartialSave(t *testing.T) {
	ctx := t.Context()
	svc := NewSettingService(&claudeCustomizationRepoStub{value: `{"preset":"official"}`}, &config.Config{})
	custom := legacyPolicy("custom")
	custom["fallback_policy"] = "strict"
	for _, update := range []map[string]any{
		{"preset": "official"},
		custom,
	} {
		require.NoError(t, svc.SetClaudeCustomizationSettings(ctx, decodePolicy(t, update)))
		global, err := svc.GetClaudeCustomizationSettings(ctx)
		require.NoError(t, err)
		require.Equal(t, ClaudeModelsCatalogSynthetic, global.ModelsCatalog)
	}
}

func TestClaudeCustomizationFields_PartialSaveReadFailure(t *testing.T) {
	repo := &claudeCustomizationRepoStub{getErr: errors.New("settings unavailable")}
	svc := NewSettingService(repo, &config.Config{})
	require.ErrorIs(t, svc.SetClaudeCustomizationSettings(context.Background(), decodePolicy(t, legacyPolicy("custom"))), repo.getErr)
	require.Empty(t, repo.setVal)
}

func TestClaudeCustomizationFields_AccountOverrideIndependent(t *testing.T) {
	ctx := context.Background()
	input := legacyPolicy("custom")
	for _, f := range alignmentFields {
		input[f.key] = f.official
	}
	input["thinking_cache_control"] = "passthrough"
	svc := NewSettingService(&claudeCustomizationRepoStub{value: string(policyJSON(t, input))}, &config.Config{})
	override, err := NormalizeClaudeCustomizationOverrides(map[string]any{"thinking_cache_control": " ReJeCt "})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"thinking_cache_control": "reject"}, override)
	account := &Account{Type: AccountTypeAPIKey, Extra: map[string]any{AccountClaudeCustomizationExtraKey: override}}
	effective, _, err := svc.ResolveClaudeCustomization(ctx, account)
	require.NoError(t, err)
	input["thinking_cache_control"] = "reject"
	require.Equal(t, input, policyMap(t, effective))
	for _, invalid := range []any{"", "drop", true} {
		override["thinking_cache_control"] = invalid
		_, err := NormalizeClaudeCustomizationOverrides(override)
		require.Error(t, err)
		effective, _, err := svc.ResolveClaudeCustomization(ctx, account)
		require.NoError(t, err)
		require.Equal(t, "passthrough", policyMap(t, effective)["thinking_cache_control"])
	}
}

func TestClaudeCustomizationFields_GlobalOnlyRejectedOnAccount(t *testing.T) {
	override := map[string]any{"models_catalog": "official"}
	_, err := NormalizeClaudeCustomizationOverrides(override)
	require.ErrorContains(t, err, "models_catalog")
	cfg := DefaultClaudeCustomizationSettings()
	applyClaudeAccountOverrides(&cfg, override, AccountTypeAPIKey)
	require.Equal(t, "synthetic", policyMap(t, cfg)["models_catalog"])
}

func TestClaudeCustomizationFields_InvalidGlobalAlignment(t *testing.T) {
	for _, f := range alignmentFields {
		t.Run(f.key, func(t *testing.T) {
			input := legacyPolicy("official")
			for _, field := range alignmentFields {
				input[field.key] = field.initial
			}
			input[f.key] = "invalid"

			repo := &claudeCustomizationRepoStub{}
			svc := NewSettingService(repo, &config.Config{})
			ctx := context.Background()
			require.ErrorContains(t, svc.SetClaudeCustomizationSettings(ctx, decodePolicy(t, input)), f.key)
			require.Empty(t, repo.setVal)

			repo.value = string(policyJSON(t, input))
			got, invalid, err := svc.ClaudeCustomizationWithInvalidFields(ctx)
			require.NoError(t, err)
			require.Equal(t, []string{f.key}, invalid)
			require.Equal(t, f.initial, policyMap(t, got)[f.key])
		})
	}
}

func TestClaudeCustomizationFields_EnumRequiresOptions(t *testing.T) {
	require.Panics(t, func() {
		bindClaudeField("request_id_mode", ClaudeFieldGroupOfficialAlignment, "none",
			func(cfg *ClaudeCustomizationSettings) *string { return &cfg.RequestIDMode },
			ClaudeAlignPassthrough, ClaudeRequestIDFill)
	})
}

func TestInapplicableOverrideIsIgnored(t *testing.T) {
	a := &Account{Type: AccountTypeOAuth, Extra: map[string]any{AccountClaudeCustomizationExtraKey: map[string]any{ClaudeFieldContextManagementMissingBeta: "reject"}}}
	p, _, err := NewSettingService(nil, nil).ResolveClaudeCustomization(t.Context(), a)
	require.NoError(t, err)
	require.Equal(t, "strip", p.ContextManagementMissingBeta)
}

func TestClaudeCustomizationNullUsesDefaultTTL(t *testing.T) {
	repo := &alignmentPolicyTestRepo{}
	require.NoError(t, repo.Set(t.Context(), SettingKeyClaudeCustomization, "  null\n"))
	settings := NewSettingService(repo, nil)
	policy, invalid, err := settings.ClaudeCustomizationWithInvalidFields(t.Context())
	require.NoError(t, err)
	require.Empty(t, invalid)
	require.Equal(t, DefaultClaudeCustomizationSettings(), policy)
	require.Greater(t, time.Until(settings.alignmentSnapshot(t.Context()).expiresAt), 8*time.Second)
	for _, value := range []string{"[]", "false", "42", `"bad"`} {
		require.NoError(t, repo.Set(t.Context(), SettingKeyClaudeCustomization, value))
		_, _, err := settings.ClaudeCustomizationWithInvalidFields(t.Context())
		require.ErrorIs(t, err, ErrClaudeCustomizationCorrupt)
	}
}
