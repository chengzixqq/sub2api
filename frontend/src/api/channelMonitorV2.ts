import { apiClient } from './client'

export type MonitorRange = '90m' | '24h' | '7d' | '30d'
export type HealthState = 'unknown' | 'healthy' | 'warning' | 'critical'
/** Fine-grained score band for multi-stop green→yellow→red gradients (score0..score10). */
export type HealthScoreBand =
  | 'unknown'
  | 'score0'
  | 'score1'
  | 'score2'
  | 'score3'
  | 'score4'
  | 'score5'
  | 'score6'
  | 'score7'
  | 'score8'
  | 'score9'
  | 'score10'
export type MonitorMatrixGroupBy = 'platform' | 'platform_group' | 'platform_model' | 'platform_group_model'

export interface MonitorFilter {
  range: MonitorRange
  platforms: string[]
  groupIds: number[]
  models: string[]
}

export interface LatencyMetric {
  sample_count: number
  p50_ms: number | null
  p90_ms?: number | null
  p95_ms: number | null
  avg_ms: number | null
}

export interface MonitorMetric {
  success_requests: number
  error_requests: number
  request_count: number
  token_count: number
  rpm: number
  tpm: number
  error_rate: number
  cache_rate: number
  cache_rate_numerator: number
  cache_rate_denominator: number
  ttft: LatencyMetric
  duration: LatencyMetric
  upstream_affected_requests?: number
  upstream_attempt_count?: number
}

export interface MonitorHealth {
  overall: HealthState
  error_rate: HealthState
  ttft: HealthState
  cache?: HealthState
  /** 0–100 blended score when samples are sufficient. */
  score?: number | null
  error_rate_score?: number | null
  ttft_score?: number | null
  cache_score?: number | null
  minimum_sample: number
  thresholds?: {
    minimum_sample?: number
    warning_error_rate: number
    critical_error_rate: number
    target_ttft_ms: number
    warning_ttft_ms: number
    critical_ttft_ms: number
    warning_cache_rate?: number
    critical_cache_rate?: number
    error_weight: number
    ttft_weight: number
    cache_weight?: number
  }
}

/** First-upgrade historical fill for 90m/24h/7d/30d; omitted when complete. */
export interface MonitorBootstrap {
  active: boolean
  progress_percent: number
  covered_from?: string
  target_start?: string
}

export interface MonitorCoverage {
  requested_start: string
  /** Exclusive upper bound of the UI-selected range (filter end). */
  requested_end?: string
  coverage_start: string
  data_through: string
  computed_at: string
  aggregation_lag_seconds: number
  coverage_complete: boolean
  bucket_seconds: number
  /** Present while initial aggregation has not covered the 30d product window. */
  bootstrap?: MonitorBootstrap | null
}

export interface MonitorConfig {
  version: number
  enabled: boolean
  refresh_interval_seconds: 60 | 300
  platforms: Array<{ platform: string; enabled: boolean; models: string[] }>
  group_ids: number[]
  health_thresholds: {
    minimum_sample: number
    warning_error_rate: number
    critical_error_rate: number
    target_ttft_ms: number
    warning_ttft_ms: number
    critical_ttft_ms: number
    warning_cache_rate: number
    critical_cache_rate: number
    error_weight: number
    ttft_weight: number
    cache_weight: number
  }
  /** Categories excluded from error_rate / health; still listed in error breakdown. */
  ignored_error_categories?: string[]
}

/** Ordered taxonomy mirrored from backend ChannelMonitorV2ErrorCategories. */
export const MONITOR_ERROR_CATEGORIES = [
  'content_policy',
  'authentication',
  'context_limit',
  'invalid_request',
  'model_unsupported',
  'group_access',
  'quota_or_balance',
  'account_pool_unavailable',
  'rate_or_capacity',
  'timeout',
  'transport_or_stream',
  'upstream_forbidden',
  'not_found',
  'client_cancelled',
  'upstream_5xx',
  'internal',
  'other',
] as const

export type MonitorErrorCategory = (typeof MONITOR_ERROR_CATEGORIES)[number]

export interface MonitorSnapshot {
  config: MonitorConfig
  coverage: MonitorCoverage
  metrics: MonitorMetric
  health: MonitorHealth
  trend: Array<{ bucket_start: string; metrics: MonitorMetric; health: MonitorHealth }>
}

export interface MonitorMatrixBucket {
  bucket_start: string
  metrics: MonitorMetric
  health: MonitorHealth
}

export interface MonitorMatrixRow {
  platform: string
  group_id?: number
  group_name?: string
  model?: string
  metrics: MonitorMetric
  health: MonitorHealth
  buckets: MonitorMatrixBucket[]
}

export interface MonitorMatrixResponse {
  coverage: MonitorCoverage
  group_by: MonitorMatrixGroupBy
  items: MonitorMatrixRow[]
}

export interface MonitorDimensions {
  platforms: Array<{ value: string; label: string; request_count: number }>
  groups: Array<{ id: number; name: string; platform?: string; request_count: number }>
  models: Array<{ value: string; label: string; platform?: string; request_count: number }>
}

export interface MonitorModelRow { platform: string; model: string; metrics: MonitorMetric; health: MonitorHealth }
export interface MonitorErrorRow {
  category: string
  count: number
  rate: number
  details?: Array<{
    platform?: string
    model?: string
    error_type?: string
    status_code?: number
    upstream_status_code?: number
    message?: string
    count: number
  }>
  /** true when category is in config.ignored_error_categories */
  ignored?: boolean
}
export interface MonitorUserRow {
  user_id?: number
  rank: number
  email?: string
  username?: string
  display_label: string
  is_self: boolean
  can_drilldown: boolean
  metrics: MonitorMetric
}

function params(filter: MonitorFilter) {
  return {
    range: filter.range,
    platform: filter.platforms.length ? filter.platforms : undefined,
    group_id: filter.groupIds.length ? filter.groupIds : undefined,
    model: filter.models.length ? filter.models : undefined,
  }
}

export function repeatedArrayParamsSerializer(values: Record<string, unknown>): string {
  const search = new URLSearchParams()
  for (const [key, rawValue] of Object.entries(values)) {
    if (rawValue == null || rawValue === '') continue
    const entries = Array.isArray(rawValue) ? rawValue : [rawValue]
    for (const value of entries) {
      if (value != null && value !== '') search.append(key, String(value))
    }
  }
  return search.toString()
}

const requestConfig = (filter: MonitorFilter, signal?: AbortSignal, extraParams: Record<string, unknown> = {}) => ({
  params: { ...params(filter), ...extraParams },
  paramsSerializer: { serialize: repeatedArrayParamsSerializer },
  signal,
})

function base(admin: boolean) { return admin ? '/admin/channel-monitor-v2' : '/channel-monitor-v2' }

export async function getDimensions(filter: MonitorFilter, admin = false, signal?: AbortSignal) {
  const { data } = await apiClient.get<MonitorDimensions>(`${base(admin)}/dimensions`, requestConfig(filter, signal))
  return data
}
export async function getSnapshot(filter: MonitorFilter, admin = false, signal?: AbortSignal) {
  const { data } = await apiClient.get<MonitorSnapshot>(`${base(admin)}/snapshot`, requestConfig(filter, signal))
  return data
}
export async function getMatrix(filter: MonitorFilter, groupBy: MonitorMatrixGroupBy, admin = false, signal?: AbortSignal) {
  const { data } = await apiClient.get<MonitorMatrixResponse>(`${base(admin)}/matrix`, requestConfig(filter, signal, { group_by: groupBy }))
  return data
}
export async function getModels(filter: MonitorFilter, admin = false, signal?: AbortSignal) {
  const { data } = await apiClient.get<{ coverage: MonitorCoverage; items: MonitorModelRow[] }>(`${base(admin)}/models`, requestConfig(filter, signal))
  return data
}
export async function getErrors(filter: MonitorFilter, admin = false, signal?: AbortSignal) {
  const { data } = await apiClient.get<{ coverage: MonitorCoverage; items: MonitorErrorRow[] }>(`${base(admin)}/errors`, requestConfig(filter, signal))
  return data
}
export async function getUsers(filter: MonitorFilter, admin = false, signal?: AbortSignal) {
  const { data } = await apiClient.get<{ coverage: MonitorCoverage; items: MonitorUserRow[] }>(`${base(admin)}/users`, requestConfig(filter, signal))
  return data
}
export async function getConfig() {
  const { data } = await apiClient.get<MonitorConfig>('/admin/channel-monitor-v2/config')
  return data
}
export async function updateConfig(config: MonitorConfig) {
  const { data } = await apiClient.put<MonitorConfig>('/admin/channel-monitor-v2/config', config)
  return data
}

export interface ObservationMetrics {
  success_requests?: number
  channel_errors?: number
  client_errors?: number
  cancelled_requests?: number
  /** Legacy combined failure aggregates; not classified as channel errors. */
  unclassified_attempts?: number
  unknown_requests?: number
  request_count?: number
  success_rate: number | null
  reliability_rate: number | null
  sample_state: 'no_samples' | 'insufficient' | 'sufficient'
  cache_rate: number | null
  ttft: LatencyMetric
  duration: LatencyMetric
  rpm?: number
  tpm?: number
  retry_recovered_requests?: number
  attempt_count?: number
  phase_avg_ms?: Record<string, number>
  error_categories?: Record<string, number>
}
export interface ObservationHealth { reliability: HealthState; latency: HealthState }
export interface ObservationCurrentStatus {
  state: HealthState | 'stale'
  source: 'traffic' | 'probe' | 'none'
  updated_at?: string
  reason: string
}
export interface ObservationProbe {
  status: string
  last_checked_at?: string
  consecutive_failures: number
}
export interface ObservationQuota { status: string; updated_at?: string }
export interface ObservationBucket { bucket_start: string; metrics: ObservationMetrics; health: ObservationHealth }
export interface ObservationModel {
  model: string
  metrics: ObservationMetrics
  traffic?: ObservationMetrics
  health: ObservationHealth
  current_status?: ObservationCurrentStatus
  probe?: ObservationProbe
  quota?: ObservationQuota
  buckets: ObservationBucket[]
}
export interface ObservationChannel {
  source?: 'compact' | 'legacy'
  platform: string
  group_id: number
  group_name: string
  rate_multiplier?: number
  metrics: ObservationMetrics
  traffic?: ObservationMetrics
  health: ObservationHealth
  current_status?: ObservationCurrentStatus
  probe?: ObservationProbe
  quota?: ObservationQuota
  buckets: ObservationBucket[]
  models: ObservationModel[]
}
export interface ObservationOverview {
  contract_version: 2
  source: 'compact' | 'legacy' | 'mixed' | 'terminal_v1'
  mode: 'shadow' | 'live'
  coverage: MonitorCoverage & {
    state: 'complete' | 'partial' | 'stale' | 'unavailable'
    source_started_at?: string
    detail_retention_hours: number
    unsupported_protocols: string[]
    collector_state?: 'healthy' | 'stale' | 'write_failed' | 'disabled' | 'backlogged'
    pending_events?: number
    last_ingested_at?: string
    gap_reasons?: string[]
  }
  dimensions: MonitorDimensions
  items: ObservationChannel[]
}
export interface ObservationConfig {
  version?: number
  enabled: boolean
  probe_enabled?: boolean
  quota_enabled?: boolean
  mode: 'shadow' | 'live'
  live_group_ids?: number[]
  detail_retention_hours: number
  refresh_interval_seconds: number
  minimum_sample: number
  healthy_reliability: number
  warning_reliability: number
  warning_ttft_ms: number
  critical_ttft_ms: number
  overrides: Array<{
    platform?: string
    group_id?: number
    model?: string
    warning_ttft_ms: number
    critical_ttft_ms: number
  }>
}
export async function getObservationOverview(filter: MonitorFilter, admin = false, signal?: AbortSignal, options: { endTime: string; refresh?: boolean; audience?: 'admin' | 'user'; preview?: boolean } = { endTime: new Date().toISOString() }) {
  const { data } = await apiClient.get<ObservationOverview>(`${base(admin)}/overview`, requestConfig(filter, signal, {
    end_time: options.endTime,
    refresh: options.refresh || undefined,
    audience: admin && options.audience === 'user' ? 'user' : undefined,
    preview: admin && options.preview ? true : undefined,
  }))
  return data
}
export async function getObservationConfig(signal?: AbortSignal) {
  const { data } = await apiClient.get<ObservationConfig>('/admin/channel-monitor-v2/observation-config', { signal })
  return data
}
export async function updateObservationConfig(config: ObservationConfig) {
  const { data } = await apiClient.put<ObservationConfig>('/admin/channel-monitor-v2/observation-config', config)
  return data
}

export type MonitorProbeProtocol = 'openai_chat' | 'openai_responses' | 'anthropic' | 'gemini'
export interface MonitorProbeTarget {
  id: number
  group_id: number
  model: string
  protocol: MonitorProbeProtocol
  enabled: boolean
  version: number
  created_at: string
  updated_at: string
}
export interface MonitorProbeBudget {
  utc_day: string
  global_limit: number
  global_used: number
  targets: Array<{ target_id: number; limit: number; used: number }>
}
export interface MonitorProbeRun {
  id: number | string
  target_id: number
  group_id: number
  model: string
  protocol: MonitorProbeProtocol
  status: 'pending' | 'healthy' | 'degraded' | 'unavailable' | 'error'
  success?: boolean
  account_id?: number
  started_at: string
  completed_at?: string
  latency_ms?: number
  error_class?: string
  input_tokens?: number
  output_tokens?: number
  upstream_cost_usd?: number
}
export interface MonitorAccountDetail {
  items: Array<{ account_id: number; metrics: ObservationMetrics; last_seen_at?: string }>
  samples: Array<{ completed_at: string; model: string; outcome: string; error_category: string; http_status: number; account_id?: number }>
}
export async function getProbeTargets(signal?: AbortSignal) {
  const { data } = await apiClient.get<{ items: MonitorProbeTarget[]; total: number }>('/admin/channel-monitor-v2/probe-targets', { signal })
  return data
}
export async function createProbeTarget(target: Pick<MonitorProbeTarget, 'group_id' | 'model' | 'protocol' | 'enabled'>) {
  const { data } = await apiClient.post<MonitorProbeTarget>('/admin/channel-monitor-v2/probe-targets', target)
  return data
}
export async function updateProbeTarget(target: MonitorProbeTarget) {
  const { id, group_id, model, protocol, enabled, version } = target
  const { data } = await apiClient.put<MonitorProbeTarget>(`/admin/channel-monitor-v2/probe-targets/${id}`, { group_id, model, protocol, enabled, version })
  return data
}
export async function runProbeTarget(id: number, idempotencyKey: string) {
  const { data } = await apiClient.post<MonitorProbeRun>(`/admin/channel-monitor-v2/probe-targets/${id}/probe`, undefined, { headers: { 'Idempotency-Key': idempotencyKey } })
  return data
}
export async function getProbeBudget(signal?: AbortSignal) {
  const { data } = await apiClient.get<MonitorProbeBudget>('/admin/channel-monitor-v2/probe-budget', { signal })
  return data
}
export async function getMonitorAccountDetail(groupId: number, filter: MonitorFilter, signal?: AbortSignal) {
  const { data } = await apiClient.get<MonitorAccountDetail>(`/admin/channel-monitor-v2/groups/${groupId}/accounts`, {
    ...requestConfig(filter, signal), params: { range: filter.range, model: filter.models.length ? filter.models : undefined },
  })
  return data
}
