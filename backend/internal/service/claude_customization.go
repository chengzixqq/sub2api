package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
)

// SettingKeyClaudeCustomization stores the global Claude compatibility policy.
// The value is JSON so new policy fields can be added without a schema migration.
const SettingKeyClaudeCustomization = "claude_customization_settings"

// AccountClaudeCustomizationExtraKey stores sparse per-account overrides in
// accounts.extra. A missing key (or a null field) means inherit from global.
const AccountClaudeCustomizationExtraKey = "claude_customization_overrides"

const claudeCustomizationRequestContextKey = "claude_customization_policy"

const (
	ClaudePresetOfficial = "official"
	ClaudePresetCustom   = "custom"
	ClaudePresetMagic    = "magic"

	ClaudeFallbackStrict            = "strict"
	ClaudeFallbackNativePassthrough = "native_passthrough"
	ClaudeFallbackFablePassthrough  = "fable_native_passthrough"
	ClaudeBetaOfficialStrict        = "official_strict"
	ClaudeBetaCapabilityAware       = "capability_aware"
	ClaudeBetaClientPassthrough     = "client_passthrough"
	ClaudeUnknownBetaFilter         = "filter"
	ClaudeUnknownBetaNativeOnly     = "pass_on_native_only"
	ClaudeUnknownBetaPass           = "pass"
)

// ClaudeCustomizationSettings is the fully materialized policy used by the
// gateway. Boolean fields are concrete after applying defaults and overrides.
type ClaudeCustomizationSettings struct {
	Preset                            string `json:"preset"`
	FallbackPolicy                    string `json:"fallback_policy"`
	ThinkingPrefilterEnabled          bool   `json:"thinking_prefilter_enabled"`
	ThinkingSignatureRetryEnabled     bool   `json:"thinking_signature_retry_enabled"`
	ThinkingToolDowngradeRetryEnabled bool   `json:"thinking_tool_downgrade_retry_enabled"`
	BetaPolicyMode                    string `json:"beta_policy_mode"`
	UnknownBetaAction                 string `json:"unknown_beta_action"`
	FingerprintUnification            bool   `json:"fingerprint_unification"`
	MetadataPassthrough               bool   `json:"metadata_passthrough"`
	URLRedactionEnabled               bool   `json:"url_redaction_enabled"`
}

// ClaudeCustomizationResponse includes the global policy, the sparse account
// override map, and the effective policy for an account-scoped read.
type ClaudeCustomizationResponse struct {
	Global    ClaudeCustomizationSettings `json:"global"`
	Overrides map[string]any              `json:"overrides,omitempty"`
	Effective ClaudeCustomizationSettings `json:"effective"`
}

func DefaultClaudeCustomizationSettings() ClaudeCustomizationSettings {
	return ClaudeCustomizationSettings{
		Preset:                            ClaudePresetMagic,
		FallbackPolicy:                    ClaudeFallbackNativePassthrough,
		ThinkingPrefilterEnabled:          false,
		ThinkingSignatureRetryEnabled:     true,
		ThinkingToolDowngradeRetryEnabled: true,
		BetaPolicyMode:                    ClaudeBetaCapabilityAware,
		UnknownBetaAction:                 ClaudeUnknownBetaNativeOnly,
		FingerprintUnification:            true,
		MetadataPassthrough:               true,
		URLRedactionEnabled:               true,
	}
}

func officialClaudeCustomizationSettings() ClaudeCustomizationSettings {
	cfg := DefaultClaudeCustomizationSettings()
	cfg.Preset = ClaudePresetOfficial
	cfg.FallbackPolicy = ClaudeFallbackStrict
	cfg.ThinkingPrefilterEnabled = true
	cfg.BetaPolicyMode = ClaudeBetaOfficialStrict
	cfg.UnknownBetaAction = ClaudeUnknownBetaFilter
	cfg.MetadataPassthrough = false
	return cfg
}

func normalizeClaudeCustomizationSettings(cfg *ClaudeCustomizationSettings) error {
	if cfg == nil {
		return fmt.Errorf("customization settings cannot be nil")
	}
	cfg.Preset = strings.ToLower(strings.TrimSpace(cfg.Preset))
	if cfg.Preset == "" {
		cfg.Preset = ClaudePresetMagic
	}
	switch cfg.Preset {
	case ClaudePresetOfficial, ClaudePresetCustom, ClaudePresetMagic:
	default:
		return fmt.Errorf("invalid preset %q", cfg.Preset)
	}
	cfg.FallbackPolicy = strings.ToLower(strings.TrimSpace(cfg.FallbackPolicy))
	switch cfg.FallbackPolicy {
	case ClaudeFallbackStrict, ClaudeFallbackNativePassthrough, ClaudeFallbackFablePassthrough:
	default:
		return fmt.Errorf("invalid fallback_policy %q", cfg.FallbackPolicy)
	}
	cfg.BetaPolicyMode = strings.ToLower(strings.TrimSpace(cfg.BetaPolicyMode))
	switch cfg.BetaPolicyMode {
	case ClaudeBetaOfficialStrict, ClaudeBetaCapabilityAware, ClaudeBetaClientPassthrough:
	default:
		return fmt.Errorf("invalid beta_policy_mode %q", cfg.BetaPolicyMode)
	}
	cfg.UnknownBetaAction = strings.ToLower(strings.TrimSpace(cfg.UnknownBetaAction))
	switch cfg.UnknownBetaAction {
	case ClaudeUnknownBetaFilter, ClaudeUnknownBetaNativeOnly, ClaudeUnknownBetaPass:
	default:
		return fmt.Errorf("invalid unknown_beta_action %q", cfg.UnknownBetaAction)
	}
	return nil
}

func applyClaudePreset(cfg *ClaudeCustomizationSettings, preset string) error {
	var next ClaudeCustomizationSettings
	switch strings.ToLower(strings.TrimSpace(preset)) {
	case ClaudePresetOfficial:
		next = officialClaudeCustomizationSettings()
	case ClaudePresetMagic:
		next = DefaultClaudeCustomizationSettings()
	case ClaudePresetCustom:
		if cfg == nil {
			next = DefaultClaudeCustomizationSettings()
		} else {
			next = *cfg
		}
		next.Preset = ClaudePresetCustom
	default:
		return fmt.Errorf("invalid preset %q", preset)
	}
	*cfg = next
	return normalizeClaudeCustomizationSettings(cfg)
}

func (s *SettingService) GetClaudeCustomizationSettings(ctx context.Context) (ClaudeCustomizationSettings, error) {
	cfg := DefaultClaudeCustomizationSettings()
	if s == nil || s.settingRepo == nil {
		return cfg, nil
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyClaudeCustomization)
	if err != nil {
		if err == ErrSettingNotFound {
			return cfg, nil
		}
		return cfg, err
	}
	if strings.TrimSpace(raw) == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return DefaultClaudeCustomizationSettings(), fmt.Errorf("decode claude customization: %w", err)
	}
	if cfg.Preset == ClaudePresetOfficial || cfg.Preset == ClaudePresetMagic {
		if err := applyClaudePreset(&cfg, cfg.Preset); err != nil {
			return DefaultClaudeCustomizationSettings(), err
		}
	}
	if err := normalizeClaudeCustomizationSettings(&cfg); err != nil {
		return DefaultClaudeCustomizationSettings(), err
	}
	return cfg, nil
}

func (s *SettingService) SetClaudeCustomizationSettings(ctx context.Context, cfg ClaudeCustomizationSettings) error {
	if cfg.Preset == ClaudePresetOfficial || cfg.Preset == ClaudePresetMagic {
		if err := applyClaudePreset(&cfg, cfg.Preset); err != nil {
			return err
		}
	} else if err := normalizeClaudeCustomizationSettings(&cfg); err != nil {
		return err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if s == nil || s.settingRepo == nil {
		return fmt.Errorf("setting repository is not configured")
	}
	if err := s.settingRepo.Set(ctx, SettingKeyClaudeCustomization, string(raw)); err != nil {
		return err
	}
	return nil
}

// ResolveClaudeCustomization applies sparse account overrides over global.
// Only known high-risk compatibility fields are accepted; unknown keys are
// ignored so old/new nodes can share the same accounts.extra payload safely.
func (s *SettingService) ResolveClaudeCustomization(ctx context.Context, account *Account) (ClaudeCustomizationSettings, map[string]any, error) {
	global, err := s.GetClaudeCustomizationSettings(ctx)
	if err != nil {
		return global, nil, err
	}
	overrides := map[string]any{}
	if account == nil || account.Extra == nil {
		return global, overrides, nil
	}
	raw, ok := account.Extra[AccountClaudeCustomizationExtraKey]
	if !ok || raw == nil {
		return global, overrides, nil
	}
	bytes, err := json.Marshal(raw)
	if err != nil {
		return global, nil, err
	}
	if err := json.Unmarshal(bytes, &overrides); err != nil {
		return global, nil, fmt.Errorf("decode account claude customization: %w", err)
	}
	applyClaudeCustomizationOverrides(&global, overrides)
	return global, overrides, nil
}

// ResolveClaudeCustomizationForRequest resolves the effective Claude policy
// once per gateway request. The policy is cached on gin.Context so retries do
// not perform additional settings/JSON reads and all retry attempts use the
// same account > global decision.
func (s *SettingService) ResolveClaudeCustomizationForRequest(ctx context.Context, c *gin.Context, account *Account) ClaudeCustomizationSettings {
	defaults := DefaultClaudeCustomizationSettings()
	if s == nil {
		return defaults
	}
	if c != nil {
		if value, ok := c.Get(claudeCustomizationRequestContextKey); ok {
			if policy, ok := value.(ClaudeCustomizationSettings); ok {
				return policy
			}
		}
	}
	policy, _, err := s.ResolveClaudeCustomization(ctx, account)
	if err != nil {
		policy = defaults
	}
	if c != nil {
		c.Set(claudeCustomizationRequestContextKey, policy)
	}
	return policy
}

func applyClaudeCustomizationOverrides(cfg *ClaudeCustomizationSettings, overrides map[string]any) {
	if cfg == nil {
		return
	}
	if v, ok := overrides["fallback_policy"].(string); ok && isClaudeFallbackPolicy(v) {
		cfg.FallbackPolicy = v
	}
	if v, ok := overrides["thinking_prefilter_enabled"].(bool); ok {
		cfg.ThinkingPrefilterEnabled = v
	}
	if v, ok := overrides["thinking_signature_retry_enabled"].(bool); ok {
		cfg.ThinkingSignatureRetryEnabled = v
	}
	if v, ok := overrides["thinking_tool_downgrade_retry_enabled"].(bool); ok {
		cfg.ThinkingToolDowngradeRetryEnabled = v
	}
	if v, ok := overrides["beta_policy_mode"].(string); ok && isClaudeBetaPolicyMode(v) {
		cfg.BetaPolicyMode = v
	}
	if v, ok := overrides["unknown_beta_action"].(string); ok && isClaudeUnknownBetaAction(v) {
		cfg.UnknownBetaAction = v
	}
	if v, ok := overrides["fingerprint_unification"].(bool); ok {
		cfg.FingerprintUnification = v
	}
	if v, ok := overrides["metadata_passthrough"].(bool); ok {
		cfg.MetadataPassthrough = v
	}
	if v, ok := overrides["url_redaction_enabled"].(bool); ok {
		cfg.URLRedactionEnabled = v
	}
	// Account overrides are deliberately unable to change the preset or any
	// protocol/security boundary. Invalid values fall back to global behavior.
	_ = normalizeClaudeCustomizationSettings(cfg)
}

func NormalizeClaudeCustomizationOverrides(input map[string]any) (map[string]any, error) {
	if input == nil {
		return map[string]any{}, nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		switch key {
		case "fallback_policy":
			v, ok := value.(string)
			if !ok || !isClaudeFallbackPolicy(v) {
				return nil, fmt.Errorf("invalid fallback_policy")
			}
			out[key] = strings.ToLower(strings.TrimSpace(v))
		case "beta_policy_mode":
			v, ok := value.(string)
			if !ok || !isClaudeBetaPolicyMode(v) {
				return nil, fmt.Errorf("invalid beta_policy_mode")
			}
			out[key] = strings.ToLower(strings.TrimSpace(v))
		case "unknown_beta_action":
			v, ok := value.(string)
			if !ok || !isClaudeUnknownBetaAction(v) {
				return nil, fmt.Errorf("invalid unknown_beta_action")
			}
			out[key] = strings.ToLower(strings.TrimSpace(v))
		case "thinking_prefilter_enabled", "thinking_signature_retry_enabled", "thinking_tool_downgrade_retry_enabled", "fingerprint_unification", "metadata_passthrough", "url_redaction_enabled":
			if value == nil {
				out[key] = nil
				continue
			}
			if _, ok := value.(bool); !ok {
				return nil, fmt.Errorf("%s must be boolean or null", key)
			}
			out[key] = value
		default:
			return nil, fmt.Errorf("unknown customization override %q", key)
		}
	}
	return out, nil
}

func isClaudeFallbackPolicy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case ClaudeFallbackStrict, ClaudeFallbackNativePassthrough, ClaudeFallbackFablePassthrough:
		return true
	default:
		return false
	}
}
func isClaudeBetaPolicyMode(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case ClaudeBetaOfficialStrict, ClaudeBetaCapabilityAware, ClaudeBetaClientPassthrough:
		return true
	default:
		return false
	}
}
func isClaudeUnknownBetaAction(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case ClaudeUnknownBetaFilter, ClaudeUnknownBetaNativeOnly, ClaudeUnknownBetaPass:
		return true
	default:
		return false
	}
}
