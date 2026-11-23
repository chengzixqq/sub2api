package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// SettingKeyClaudeCustomization stores the global Claude compatibility policy.
// The value is JSON so new policy fields can be added without a schema migration.
const SettingKeyClaudeCustomization = "claude_customization_settings"

var ErrClaudeCustomizationCorrupt = errors.New("stored Claude customization is not a JSON object")

// AccountClaudeCustomizationExtraKey stores sparse per-account overrides in
// accounts.extra. A missing key (or a null field) means inherit from global.
const AccountClaudeCustomizationExtraKey = "claude_customization_overrides"

const claudeCustomizationRequestContextKey = "claude_customization_policy"

type claudeCustomizationRequestPolicy struct {
	accountID int64
	policy    ClaudeCustomizationSettings
}

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

	// ClaudeCacheControlLimitTrim keeps the historical behavior: requests with
	// more than four cache_control breakpoints are trimmed before forwarding.
	ClaudeCacheControlLimitTrim = "trim"
	// ClaudeCacheControlLimitPassthrough forwards a client request that already
	// exceeds four breakpoints unchanged, so the upstream returns its own 400.
	ClaudeCacheControlLimitPassthrough = "passthrough"
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
	// CacheControlLimitMode controls what the native Claude /v1/messages and
	// count_tokens paths do when the client sends more than four cache_control
	// breakpoints. Protocol conversion paths always trim.
	CacheControlLimitMode        string `json:"cache_control_limit_mode"`
	ContextManagementMissingBeta string `json:"context_management_missing_beta"`
	RequestIDMode                string `json:"request_id_mode"`
	UnknownBetaHeader            string `json:"unknown_beta_header"`
	ModelsCatalog                string `json:"models_catalog"`
	UpstreamHeaderPassthrough    string `json:"upstream_header_passthrough"`
	ThinkingCacheControl         string `json:"thinking_cache_control"`
	BedrockCCContextManagement   string `json:"bedrock_cc_context_management"`
}

// ClaudeCustomizationResponse includes the global policy, the sparse account
// override map, and the effective policy for an account-scoped read.
type ClaudeCustomizationResponse struct {
	Global           ClaudeCustomizationSettings `json:"global"`
	Overrides        map[string]any              `json:"overrides,omitempty"`
	IgnoredOverrides []string                    `json:"ignored_overrides,omitempty"`
	Effective        ClaudeCustomizationSettings `json:"effective"`
}

var defaultClaudeCustomization = func() ClaudeCustomizationSettings {
	cfg := ClaudeCustomizationSettings{Preset: ClaudePresetMagic}
	for _, f := range claudeCustomizationRegistry {
		f.set(&cfg, f.Default)
	}
	return cfg
}()

func DefaultClaudeCustomizationSettings() ClaudeCustomizationSettings {
	return defaultClaudeCustomization
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
	for _, f := range claudeCustomizationRegistry {
		if f.Kind != "enum" {
			continue
		}
		raw, ok := f.get(cfg).(string)
		if !ok {
			return fmt.Errorf("%s must be a string", f.Key)
		}
		value, valid := f.normalizeEnum(raw)
		if !f.PresetManaged && value == "" {
			f.set(cfg, f.Default)
			continue
		}
		if !valid {
			return fmt.Errorf("invalid %s %q", f.Key, value)
		}
		f.set(cfg, value)
	}
	return nil
}

func applyClaudePreset(cfg *ClaudeCustomizationSettings, preset string) error {
	if cfg == nil {
		return fmt.Errorf("customization settings cannot be nil")
	}
	name := strings.ToLower(strings.TrimSpace(preset))
	switch name {
	case ClaudePresetMagic, ClaudePresetOfficial:
		for _, f := range claudeCustomizationRegistry {
			if f.PresetManaged {
				value := f.MagicValue
				if name == ClaudePresetOfficial {
					value = f.OfficialValue
				}
				f.set(cfg, value)
			}
		}
	case ClaudePresetCustom:
	default:
		return fmt.Errorf("invalid preset %q", preset)
	}
	cfg.Preset = name
	return normalizeClaudeCustomizationSettings(cfg)
}

func materializeClaudeCustomization(cfg *ClaudeCustomizationSettings) error {
	if cfg.Preset == ClaudePresetOfficial || cfg.Preset == ClaudePresetMagic {
		return applyClaudePreset(cfg, cfg.Preset)
	}
	return normalizeClaudeCustomizationSettings(cfg)
}

func (s *SettingService) GetClaudeCustomizationSettings(ctx context.Context) (ClaudeCustomizationSettings, error) {
	cfg, _, err := s.ClaudeCustomizationWithInvalidFields(ctx)
	return cfg, err
}

func (s *SettingService) ClaudeCustomizationWithInvalidFields(ctx context.Context) (ClaudeCustomizationSettings, []string, error) {
	cfg := DefaultClaudeCustomizationSettings()
	if s == nil || s.settingRepo == nil {
		return cfg, nil, nil
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyClaudeCustomization)
	if errors.Is(err, ErrSettingNotFound) {
		return cfg, nil, nil
	}
	if err != nil || strings.TrimSpace(raw) == "" {
		return cfg, nil, err
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return cfg, nil, fmt.Errorf("decode claude customization: %w", errors.Join(ErrClaudeCustomizationCorrupt, err))
	}
	if fields == nil { // JSON null is the legacy empty setting.
		return cfg, nil, nil
	}
	var invalid []string
	if value := fields["preset"]; value != nil {
		name, _ := value.(string)
		switch name = strings.ToLower(strings.TrimSpace(name)); name {
		case ClaudePresetCustom, ClaudePresetOfficial, ClaudePresetMagic:
			cfg.Preset = name
		default:
			cfg.Preset = ClaudePresetCustom
			invalid = append(invalid, "preset")
		}
	}
	for _, f := range claudeCustomizationRegistry {
		value := fields[f.Key]
		if value == nil {
			continue
		}
		normalized, err := f.normalizeOverride(value)
		if err != nil {
			invalid = append(invalid, f.Key)
			continue
		}
		f.set(&cfg, normalized)
	}
	err = materializeClaudeCustomization(&cfg)
	return cfg, invalid, err
}

// ResetClaudeCustomizationField repairs one stored member atomically. An
// invalid preset already materializes as custom, preserving explicit fields.
func (s *SettingService) ResetClaudeCustomizationField(ctx context.Context, key string) error {
	if key != "preset" && !slices.ContainsFunc(claudeCustomizationRegistry[:], func(f claudeFieldBinding) bool { return f.Key == key }) {
		return fmt.Errorf("unknown field %q", key)
	}
	writer, ok := s.settingRepo.(interface {
		RepairClaudeCustomizationField(context.Context, string) error
	})
	if !ok {
		return errors.New("per-field settings reset unavailable")
	}
	if err := writer.RepairClaudeCustomizationField(ctx, key); err != nil {
		return err
	}
	s.invalidateAlignmentSnapshot(ctx)
	return nil
}

func (s *SettingService) SetClaudeCustomizationSettings(ctx context.Context, cfg ClaudeCustomizationSettings) error {
	// Older clients omit alignment fields. Read the current policy once, only
	// when needed, and abort the write if the values to preserve are unavailable.
	var current *ClaudeCustomizationSettings
	for _, f := range claudeCustomizationRegistry {
		if f.PresetManaged || f.Kind != "enum" {
			continue
		}
		value, ok := f.get(&cfg).(string)
		if !ok {
			return fmt.Errorf("%s must be a string", f.Key)
		}
		if strings.TrimSpace(value) != "" {
			continue
		}
		if current == nil {
			stored, err := s.GetClaudeCustomizationSettings(ctx)
			if err != nil {
				return err
			}
			current = &stored
		}
		f.set(&cfg, f.get(current))
	}
	if err := materializeClaudeCustomization(&cfg); err != nil {
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
	s.invalidateAlignmentSnapshot(ctx)
	return nil
}

// ResolveClaudeCustomization applies sparse account overrides over global.
// Unknown keys are ignored so old/new nodes can share accounts.extra.
func (s *SettingService) ResolveClaudeCustomization(ctx context.Context, account *Account) (ClaudeCustomizationSettings, map[string]any, error) {
	global, globalErr := s.GetClaudeCustomizationSettings(ctx)
	overrides := map[string]any{}
	if account == nil || account.Extra == nil {
		return global, overrides, globalErr
	}
	raw, ok := account.Extra[AccountClaudeCustomizationExtraKey]
	if !ok || raw == nil {
		return global, overrides, globalErr
	}
	bytes, err := json.Marshal(raw)
	if err != nil {
		return global, nil, errors.Join(globalErr, err)
	}
	if err := json.Unmarshal(bytes, &overrides); err != nil {
		return global, nil, errors.Join(globalErr, fmt.Errorf("decode account claude customization: %w", err))
	}
	applyClaudeAccountOverrides(&global, overrides, account.Type)
	return global, overrides, globalErr
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
			if cached, ok := value.(claudeCustomizationRequestPolicy); ok {
				var accountID int64
				if account != nil {
					accountID = account.ID
				}
				if cached.accountID == accountID {
					return cached.policy
				}
			}
		}
	}
	policy, _, err := s.ResolveClaudeCustomization(ctx, account)
	if err != nil {
		// ResolveClaudeCustomization returns the best valid lower-precedence
		// layer with its error: global when an account override is malformed,
		// or defaults when the global setting cannot be read. Keep that layer
		// instead of discarding a valid global policy.
		s.logClaudeCustomizationResolveFailure(ctx, account, err)
	}
	if c != nil {
		var accountID int64
		if account != nil {
			accountID = account.ID
		}
		c.Set(claudeCustomizationRequestContextKey, claudeCustomizationRequestPolicy{accountID: accountID, policy: policy})
		if policy.UpstreamHeaderPassthrough == ClaudeHeaderPassthroughOn && s.cfg != nil && s.cfg.Security.ResponseHeaders.Enabled && len(s.cfg.Security.ResponseHeaders.ForceRemove) != 0 {
			c.Set(officialForceRemoveKey, s.cfg.Security.ResponseHeaders.ForceRemove)
		}
	}
	return policy
}

const claudeCustomizationResolveErrorLogInterval = time.Minute

func shouldLogClaudePolicyFailure(lastLogAt *atomic.Int64) bool {
	now := time.Now().UnixNano()
	last := lastLogAt.Load()
	return (last == 0 || now-last >= int64(claudeCustomizationResolveErrorLogInterval)) && lastLogAt.CompareAndSwap(last, now)
}

func (s *SettingService) logClaudeCustomizationResolveFailure(ctx context.Context, account *Account, err error) {
	if s == nil || err == nil || !shouldLogClaudePolicyFailure(&s.claudeCustomizationErrorLogAt) {
		return
	}
	accountID := int64(0)
	if account != nil {
		accountID = account.ID
	}
	logger.FromContext(ctx).Warn(
		"claude.customization_resolve_failed; using best available policy layer",
		zap.Int64("account_id", accountID),
		zap.Error(err),
	)
}

func (f claudeFieldBinding) appliesToAccount(accountType string) bool {
	return len(f.AccountTypes) == 0 || slices.Contains(f.AccountTypes, accountType)
}
func IgnoredClaudeOverrides(accountType string, overrides map[string]any) []string {
	var keys []string
	for _, f := range claudeCustomizationRegistry {
		if overrides[f.Key] != nil && (!f.AccountOverridable || !f.appliesToAccount(accountType)) {
			keys = append(keys, f.Key)
		}
	}
	return keys
}
func applyClaudeAccountOverrides(cfg *ClaudeCustomizationSettings, overrides map[string]any, accountType string) {
	if cfg == nil {
		return
	}
	for _, f := range claudeCustomizationRegistry {
		if !f.appliesToAccount(accountType) {
			continue
		}
		value := overrides[f.Key]
		if !f.AccountOverridable || value == nil {
			continue
		}
		if normalized, err := f.normalizeOverride(value); err == nil {
			f.set(cfg, normalized)
		}
	}
}

func NormalizeClaudeCustomizationOverrides(input map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(input))
	for key, value := range input {
		index := slices.IndexFunc(claudeCustomizationRegistry[:], func(f claudeFieldBinding) bool { return f.Key == key })
		if index < 0 {
			return nil, fmt.Errorf("unknown customization override %q", key)
		}
		f := claudeCustomizationRegistry[index]
		if value == nil {
			out[key] = nil
			continue
		}
		if !f.AccountOverridable {
			return nil, fmt.Errorf("%s is global-only", key)
		}
		normalized, err := f.normalizeOverride(value)
		if err != nil {
			return nil, err
		}
		out[key] = normalized
	}
	return out, nil
}
