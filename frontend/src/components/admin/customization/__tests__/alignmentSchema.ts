import type { ClaudeCustomizationField } from '@/api/admin/settings'

function field(
  key: ClaudeCustomizationField['key'], options: string[], defaultValue: string, official: string,
  risk: ClaudeCustomizationField['risk'], hitStats: boolean, globalOnly = false,
): ClaudeCustomizationField {
  return {
    key, group: 'official_alignment', kind: 'enum', options, default: defaultValue,
    official_value: official, risk, hit_stats: hitStats, preset_managed: false,
    global_only: globalOnly, account_overridable: !globalOnly,
  }
}

export const alignmentFields = [
  field('context_management_missing_beta', ['strip', 'reject'], 'strip', 'reject', 'medium', true),
  field('request_id_mode', ['passthrough', 'fill'], 'passthrough', 'fill', 'low', false),
  field('cache_control_limit_mode', ['trim', 'passthrough', 'reject'], 'trim', 'reject', 'medium', true),
  field('unknown_beta_header', ['passthrough', 'reject'], 'passthrough', 'reject', 'high', true),
  field('models_catalog', ['synthetic', 'official'], 'synthetic', 'official', 'none', false, true),
  field('upstream_header_passthrough', ['off', 'on'], 'off', 'on', 'low', false),
  field('thinking_cache_control', ['strip', 'passthrough', 'reject'], 'strip', 'reject', 'medium', true),
  field('bedrock_cc_context_management', ['strip', 'passthrough'], 'strip', 'passthrough', 'medium', false),
]

export const alignmentDefaults = {
  context_management_missing_beta: 'strip',
  request_id_mode: 'passthrough',
  cache_control_limit_mode: 'trim',
  unknown_beta_header: 'passthrough',
  models_catalog: 'synthetic',
  upstream_header_passthrough: 'off',
  thinking_cache_control: 'strip',
  bedrock_cc_context_management: 'strip',
} as const

export const schema = [
  { ...alignmentFields[0], key: 'fallback_policy', group: 'compatibility' },
  ...alignmentFields,
]
