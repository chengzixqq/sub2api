package service

import (
	"fmt"
	"slices"
	"strings"
)

const (
	ClaudeFieldGroupOfficialAlignment = "official_alignment"
	ClaudeAlignStrip                  = "strip"
	ClaudeAlignReject                 = "reject"
	ClaudeAlignPassthrough            = "passthrough"
	ClaudeRequestIDFill               = "fill"
	ClaudeModelsCatalogOfficial       = "official"
	ClaudeModelsCatalogSynthetic      = "synthetic"
	ClaudeHeaderPassthroughOn         = "on"
	ClaudeHeaderPassthroughOff        = "off"
)

const (
	ClaudeFieldContextManagementMissingBeta = "context_management_missing_beta"
	ClaudeFieldRequestIDMode                = "request_id_mode"
	ClaudeFieldCacheControlLimitMode        = "cache_control_limit_mode"
	ClaudeFieldUnknownBetaHeader            = "unknown_beta_header"
	ClaudeFieldModelsCatalog                = "models_catalog"
	ClaudeFieldUpstreamHeaderPassthrough    = "upstream_header_passthrough"
	ClaudeFieldThinkingCacheControl         = "thinking_cache_control"
	ClaudeFieldBedrockCCContextManagement   = "bedrock_cc_context_management"
)

// ClaudeCustomizationField describes one flat setting in the admin schema.
// OfficialValue describes the official behavior even for fields outside presets.
type ClaudeCustomizationField struct {
	Key                string   `json:"key"`
	Group              string   `json:"group"`
	Kind               string   `json:"kind"`
	Options            []string `json:"options"`
	Default            any      `json:"default"`
	OfficialValue      any      `json:"official_value"`
	MagicValue         any      `json:"-"`
	PresetManaged      bool     `json:"preset_managed"`
	AccountOverridable bool     `json:"account_overridable"`
	AccountTypes       []string `json:"account_types"`
	GlobalOnly         bool     `json:"global_only"`
	Risk               string   `json:"risk"`
	HitStats           bool     `json:"hit_stats"`
}

type claudeFieldBinding struct {
	ClaudeCustomizationField
	get func(*ClaudeCustomizationSettings) any
	set func(*ClaudeCustomizationSettings, any)
}

var nativeClaudeAlignmentAccountTypes = []string{AccountTypeAPIKey, AccountTypeOAuth, AccountTypeSetupToken}

func (f claudeFieldBinding) forAccountTypes(types ...string) claudeFieldBinding {
	f.AccountTypes = types
	return f
}

func bindClaudeField[T bool | string](key, group, risk string, member func(*ClaudeCustomizationSettings) *T,
	initial, official T, options ...string) claudeFieldBinding {
	kind := "bool"
	if _, isString := any(initial).(string); isString {
		kind = "enum"
		if len(options) == 0 {
			panic(fmt.Sprintf("Claude customization enum %s requires options", key))
		}
	}
	return claudeFieldBinding{
		ClaudeCustomizationField: ClaudeCustomizationField{
			Key: key, Group: group, Kind: kind, Options: options, Risk: risk,
			Default: initial, MagicValue: initial, OfficialValue: official,
			PresetManaged: group != ClaudeFieldGroupOfficialAlignment, AccountOverridable: true,
		},
		get: func(cfg *ClaudeCustomizationSettings) any { return *member(cfg) },
		set: func(cfg *ClaudeCustomizationSettings, value any) {
			typed, ok := value.(T)
			if !ok {
				panic(fmt.Sprintf("unexpected value type %T for Claude customization field %s", value, key))
			}
			*member(cfg) = typed
		},
	}
}

func (f claudeFieldBinding) globalOnly() claudeFieldBinding {
	f.GlobalOnly = true
	f.AccountOverridable = false
	return f
}

func (f claudeFieldBinding) normalizeEnum(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	return value, slices.Contains(f.Options, value)
}

// normalizeOverride accepts the stored/API representation and returns the
// canonical value. A null boolean override is handled separately as inheritance.
func (f claudeFieldBinding) normalizeOverride(value any) (any, error) {
	if f.Kind == "bool" {
		b, ok := value.(bool)
		if !ok {
			return nil, fmt.Errorf("%s must be boolean or null", f.Key)
		}
		return b, nil
	}
	s, ok := value.(string)
	if ok {
		if normalized, valid := f.normalizeEnum(s); valid {
			return normalized, nil
		}
	}
	return nil, fmt.Errorf("invalid %s", f.Key)
}

var claudeCustomizationRegistry = [...]claudeFieldBinding{
	// fallbackPolicy controls forwarding of fallback tokens and fields. strict strips them,
	// native_passthrough and the legacy fable_native_passthrough allow native forwarding.
	// API context: https://platform.claude.com/docs/en/api/messages/create
	// 2026-11-20 design: no upstream transcript for this gateway policy.
	bindClaudeField("fallback_policy", "compatibility", "medium",
		func(s *ClaudeCustomizationSettings) *string { return &s.FallbackPolicy },
		ClaudeFallbackNativePassthrough, ClaudeFallbackStrict,
		ClaudeFallbackNativePassthrough, ClaudeFallbackStrict, ClaudeFallbackFablePassthrough),

	// betaPolicyMode selects beta policy evaluation. capability_aware adapts to the exit,
	// official_strict evaluates policy under the official preset's restrictions, and
	// client_passthrough preserves client betas subject to native context-edit restrictions.
	// API context: https://platform.claude.com/docs/en/api/beta-headers
	// 2026-11-20 design: no upstream transcript for these gateway policy modes.
	bindClaudeField("beta_policy_mode", "compatibility", "medium",
		func(s *ClaudeCustomizationSettings) *string { return &s.BetaPolicyMode },
		ClaudeBetaCapabilityAware, ClaudeBetaOfficialStrict,
		ClaudeBetaCapabilityAware, ClaudeBetaOfficialStrict, ClaudeBetaClientPassthrough),

	// unknownBetaAction handles beta tokens outside the configured rules. filter drops
	// unknown tokens, pass_on_native_only preserves them on native exits, and pass allows them.
	// API context: https://platform.claude.com/docs/en/api/beta-headers
	// 2026-11-20 design: no upstream transcript for these gateway filtering modes.
	bindClaudeField("unknown_beta_action", "compatibility", "high",
		func(s *ClaudeCustomizationSettings) *string { return &s.UnknownBetaAction },
		ClaudeUnknownBetaNativeOnly, ClaudeUnknownBetaFilter,
		ClaudeUnknownBetaNativeOnly, ClaudeUnknownBetaFilter, ClaudeUnknownBetaPass),

	// thinkingPrefilterEnabled enables pre-forward thinking history repair. False preserves history.
	// API context: https://platform.claude.com/docs/en/build-with-claude/extended-thinking
	// 2026-11-20 design: no upstream transcript for this gateway prefilter.
	bindClaudeField("thinking_prefilter_enabled", "thinking", "medium",
		func(s *ClaudeCustomizationSettings) *bool { return &s.ThinkingPrefilterEnabled }, false, true),

	// thinkingSignatureRetryEnabled rewrites and retries eligible thinking/signature errors
	// when true. False leaves this retry disabled.
	// API context: https://platform.claude.com/docs/en/build-with-claude/extended-thinking
	// 2026-11-20 design: no upstream transcript for this gateway retry policy.
	bindClaudeField("thinking_signature_retry_enabled", "thinking", "medium",
		func(s *ClaudeCustomizationSettings) *bool { return &s.ThinkingSignatureRetryEnabled }, false, false),

	// thinkingToolDowngradeRetryEnabled allows a further retry with downgraded tool
	// structures after eligible signature retries. False skips the downgrade.
	// API context: https://platform.claude.com/docs/en/build-with-claude/extended-thinking
	// 2026-11-20 design: no upstream transcript for this gateway retry policy.
	bindClaudeField("thinking_tool_downgrade_retry_enabled", "thinking", "medium",
		func(s *ClaudeCustomizationSettings) *bool { return &s.ThinkingToolDowngradeRetryEnabled }, false, false),

	// fingerprintUnification unifies SDK fingerprint headers for a shared OAuth account
	// when true. False disables unification.
	// SDK context: https://github.com/anthropics/anthropic-sdk-typescript
	// 2026-11-20 design: no upstream transcript for this gateway header policy.
	bindClaudeField("fingerprint_unification", "privacy", "low",
		func(s *ClaudeCustomizationSettings) *bool { return &s.FingerprintUnification }, true, true),

	// metadataPassthrough preserves client metadata when true. False allows compatibility
	// metadata rewriting on paths that need it.
	// API context: https://platform.claude.com/docs/en/api/messages/create
	// 2026-11-20 design: no upstream transcript for this gateway metadata policy.
	bindClaudeField("metadata_passthrough", "privacy", "low",
		func(s *ClaudeCustomizationSettings) *bool { return &s.MetadataPassthrough }, true, false),

	// urlRedactionEnabled redacts API-key upstream URLs in downstream diagnostics when
	// true. False disables redaction. The outbound URL stays the same in both cases.
	// API context: https://platform.claude.com/docs/en/api/errors
	// 2026-11-20 design: no upstream transcript for this local diagnostic policy.
	bindClaudeField("url_redaction_enabled", "privacy", "none",
		func(s *ClaudeCustomizationSettings) *bool { return &s.URLRedactionEnabled }, true, true),

	// contextManagementMissingBeta handles context_management on native API-key exits when
	// neither context beta is present. strip removes it before forwarding, reject returns 400.
	// Source: https://platform.claude.com/docs/en/build-with-claude/context-editing
	// Observed 2026-11-20: context_management: Extra inputs are not permitted
	bindClaudeField(ClaudeFieldContextManagementMissingBeta, ClaudeFieldGroupOfficialAlignment, "medium",
		func(s *ClaudeCustomizationSettings) *string { return &s.ContextManagementMissingBeta },
		ClaudeAlignStrip, ClaudeAlignReject, ClaudeAlignStrip, ClaudeAlignReject).forAccountTypes(AccountTypeAPIKey),

	// requestIDMode controls missing request IDs. passthrough keeps existing IDs, fill
	// supplies a request-id header and a matching request_id in error bodies.
	// Source: https://platform.claude.com/docs/en/api/errors
	// Observed 2026-11-20: 200 时有 `Request-Id` 头；400（字段校验类）body 里有 `request_id`，没有头；400（未知模型）、401 两者都没有。
	bindClaudeField(ClaudeFieldRequestIDMode, ClaudeFieldGroupOfficialAlignment, "low",
		func(s *ClaudeCustomizationSettings) *string { return &s.RequestIDMode },
		ClaudeAlignPassthrough, ClaudeRequestIDFill, ClaudeAlignPassthrough, ClaudeRequestIDFill).forAccountTypes(nativeClaudeAlignmentAccountTypes...),

	// cacheControlLimitMode handles excess client breakpoints. trim drops excess breakpoints,
	// passthrough lets the upstream decide, reject returns 400 for more than four client breakpoints.
	// Source: https://platform.claude.com/docs/en/build-with-claude/prompt-caching
	// Observed 2026-11-20 (N=5,6): A maximum of 4 blocks with cache_control may be provided. Found N.
	bindClaudeField(ClaudeFieldCacheControlLimitMode, ClaudeFieldGroupOfficialAlignment, "medium",
		func(s *ClaudeCustomizationSettings) *string { return &s.CacheControlLimitMode },
		ClaudeCacheControlLimitTrim, ClaudeAlignReject, ClaudeCacheControlLimitTrim, ClaudeAlignPassthrough, ClaudeAlignReject).forAccountTypes(nativeClaudeAlignmentAccountTypes...),

	// unknownBetaHeader controls validation of client beta names. passthrough permits them,
	// reject returns 400 for unknown names. Tokens explicitly configured in beta rules are known.
	// Source: https://platform.claude.com/docs/en/api/beta-headers
	// Observed 2026-11-20 (one unknown accepted, two rejected):
	// Unexpected value(s) `aaa-bad-1`, `bbb-bad-2` for the `anthropic-beta` header. Please consult our documentation at platform.claude.com/docs or try again without the header.
	bindClaudeField(ClaudeFieldUnknownBetaHeader, ClaudeFieldGroupOfficialAlignment, "high",
		func(s *ClaudeCustomizationSettings) *string { return &s.UnknownBetaHeader },
		ClaudeAlignPassthrough, ClaudeAlignReject, ClaudeAlignPassthrough, ClaudeAlignReject).forAccountTypes(nativeClaudeAlignmentAccountTypes...),

	// modelsCatalog selects the pre-account Claude model list. synthetic uses the gateway
	// list, official supplies documented release days, lifecycle and pagination.
	// Unpublished per-model capabilities remain null.
	// Source: https://platform.claude.com/docs/en/api/models/list
	// On 2026-11-20 the probed upstream returned one fixed created_at for every ID.
	// For group-private models the official documentation permits an epoch fallback:
	// "May be set to an epoch value if the release date is unknown"
	bindClaudeField(ClaudeFieldModelsCatalog, ClaudeFieldGroupOfficialAlignment, "none",
		func(s *ClaudeCustomizationSettings) *string { return &s.ModelsCatalog },
		ClaudeModelsCatalogSynthetic, ClaudeModelsCatalogOfficial, ClaudeModelsCatalogSynthetic, ClaudeModelsCatalogOfficial).forAccountTypes(nativeClaudeAlignmentAccountTypes...).globalOnly(),

	// upstreamHeaderPassthrough controls forwarding of upstream request-id and rate-limit
	// headers. off keeps current forwarding, on adds available official headers. Both
	// retain retry-after and protect the upstream organization ID.
	// Source: https://platform.claude.com/docs/en/api/rate-limits
	// Observed 2026-11-20: 被测上游只返回 request-id，不返回 ratelimit 头，所以对这类上游开启后，实际只多出 request-id。
	bindClaudeField(ClaudeFieldUpstreamHeaderPassthrough, ClaudeFieldGroupOfficialAlignment, "low",
		func(s *ClaudeCustomizationSettings) *string { return &s.UpstreamHeaderPassthrough },
		ClaudeHeaderPassthroughOff, ClaudeHeaderPassthroughOn, ClaudeHeaderPassthroughOff, ClaudeHeaderPassthroughOn).forAccountTypes(nativeClaudeAlignmentAccountTypes...),

	// thinkingCacheControl handles cache_control on historical thinking/redacted_thinking
	// message blocks. strip removes it, passthrough forwards it, reject returns the first
	// offending block as a local 400.
	// Source: https://platform.claude.com/docs/en/api/messages/create
	// Observed 2026-11-20:
	// {"type":"error","error":{"type":"invalid_request_error","message":"messages.1.content.0.thinking.cache_control: Extra inputs are not permitted"},"request_id":"req_..."}
	bindClaudeField(ClaudeFieldThinkingCacheControl, ClaudeFieldGroupOfficialAlignment, "medium",
		func(s *ClaudeCustomizationSettings) *string { return &s.ThinkingCacheControl },
		ClaudeAlignStrip, ClaudeAlignReject, ClaudeAlignStrip, ClaudeAlignPassthrough, ClaudeAlignReject).forAccountTypes(nativeClaudeAlignmentAccountTypes...),

	// bedrockCCContextManagement controls context_management only on channels with
	// Bedrock CC compatibility enabled. strip keeps production behavior; passthrough
	// preserves the field with the final context-management beta. Compact always survives.
	// Source: https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters-anthropic-claude-messages-request-response.html
	// 2026-11-21: the previous release strips the field; no new upstream probe for this switch.
	bindClaudeField(ClaudeFieldBedrockCCContextManagement, ClaudeFieldGroupOfficialAlignment, "medium",
		func(s *ClaudeCustomizationSettings) *string { return &s.BedrockCCContextManagement },
		ClaudeAlignStrip, ClaudeAlignPassthrough, ClaudeAlignStrip, ClaudeAlignPassthrough),
}

// ClaudeCustomizationFields returns a copy of the schema in admin display order.
func ClaudeCustomizationFields() []ClaudeCustomizationField {
	fields := make([]ClaudeCustomizationField, len(claudeCustomizationRegistry))
	for i, binding := range claudeCustomizationRegistry {
		fields[i] = binding.ClaudeCustomizationField
		fields[i].HitStats = slices.Contains(alignmentHitFields[:], binding.Key)
		fields[i].Options = slices.Clone(binding.Options)
		fields[i].AccountTypes = slices.Clone(binding.AccountTypes)
	}
	return fields
}
