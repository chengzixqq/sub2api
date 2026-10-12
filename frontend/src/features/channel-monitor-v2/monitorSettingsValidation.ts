import type { MonitorConfig, ObservationConfig } from '@/api/channelMonitorV2'

export const observationPolicyDefaults = { traffic_freshness_seconds: 300, probe_failure_threshold: 2, quota_stale_seconds: 600, probe_interval_seconds: 900, probe_timeout_seconds: 20, probe_target_daily_limit: 96, probe_global_daily_limit: 1000 }
export const finite = (value: unknown): value is number => typeof value === 'number' && Number.isFinite(value)
export const integerBetween = (value: unknown, min: number, max: number) => finite(value) && Number.isInteger(value) && value >= min && value <= max
export const validModel = (value: string) => /^[a-zA-Z0-9][a-zA-Z0-9_.:/+\-]{0,191}$/.test(value) && !value.includes('://')
export function validateObservation(value: ObservationConfig): string | null {
  if (!['shadow', 'live'].includes(value.mode)) return 'mode'
  if (!integerBetween(value.refresh_interval_seconds, 30, 300) || value.detail_retention_hours !== 24) return 'refresh'
  if (!integerBetween(value.traffic_freshness_seconds, 60, 3600) || !integerBetween(value.probe_failure_threshold, 1, 10) || !integerBetween(value.quota_stale_seconds, 300, 3600)) return 'freshness'
  if (!integerBetween(value.minimum_sample, 1, 1_000_000)) return 'sample'
  if (!finite(value.warning_reliability) || !finite(value.healthy_reliability) || !(value.warning_reliability > 0 && value.warning_reliability < value.healthy_reliability && value.healthy_reliability <= 1)) return 'reliability'
  const latency = (warning: unknown, critical: unknown) => integerBetween(warning, 1, 86_400_000) && integerBetween(critical, 1, 86_400_000) && Number(critical) > Number(warning)
  if (!latency(value.warning_ttft_ms, value.critical_ttft_ms)) return 'latency'
  if (!integerBetween(value.probe_interval_seconds, 60, 3600) || !integerBetween(value.probe_timeout_seconds, 5, 60) || !integerBetween(value.probe_target_daily_limit, 1, 1440) || !integerBetween(value.probe_global_daily_limit, 1, 100000)) return 'probe'
  const live = value.live_group_ids || []
  if (live.length > 1000 || new Set(live).size !== live.length || live.some(id => !integerBetween(id, 1, Number.MAX_SAFE_INTEGER))) return 'groups'
  const seen = new Set<string>()
  if (value.overrides.length > 100) return 'overrides'
  for (const rule of value.overrides) {
    const platform = rule.platform || ''
    const group = rule.group_id ?? 0
    const model = rule.model || ''
    if ((!platform && !group && !model) || !integerBetween(group, 0, Number.MAX_SAFE_INTEGER) || (platform && !/^[a-zA-Z0-9_\-]{1,64}$/.test(platform)) || (model && !validModel(model)) || !latency(rule.warning_ttft_ms, rule.critical_ttft_ms)) return 'overrides'
    const key = JSON.stringify([platform, group, model])
    if (seen.has(key)) return 'duplicate'
    seen.add(key)
  }
  return null
}
export function validateLegacy(value: MonitorConfig): string | null {
  const h = value.health_thresholds
  if (![60, 300].includes(value.refresh_interval_seconds)) return 'refresh'
  if (!integerBetween(h.minimum_sample, 1, 10000)) return 'sample'
  if (![h.warning_error_rate, h.critical_error_rate].every(v => finite(v) && v > 0 && v <= 1) || h.warning_error_rate > h.critical_error_rate) return 'errors'
  if (![h.target_ttft_ms, h.warning_ttft_ms, h.critical_ttft_ms].every(v => integerBetween(v, 1, 86400000)) || h.target_ttft_ms > h.warning_ttft_ms || h.warning_ttft_ms > h.critical_ttft_ms) return 'latency'
  if (![h.warning_cache_rate, h.critical_cache_rate].every(v => finite(v) && v >= 0 && v <= 1) || h.critical_cache_rate > h.warning_cache_rate) return 'cache'
  const weights = [h.error_weight, h.ttft_weight, h.cache_weight]
  if (!weights.every(v => finite(v) && v >= 0 && v <= 1) || Math.abs(weights.reduce((a, b) => a + b, 0) - 1) > .000001) return 'weights'
  return null
}
