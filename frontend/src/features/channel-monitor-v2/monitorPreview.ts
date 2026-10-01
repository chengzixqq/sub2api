import type {
  MonitorDimensions,
  ObservationBucket,
  ObservationChannel,
  ObservationHealth,
  ObservationMetrics,
  ObservationModel,
  ObservationOverview,
} from '@/api/channelMonitorV2'

/**
 * Local-only monitoring fixtures. They deliberately model the response
 * contract used by the real monitor so visual checks do not need a server,
 * credentials, or an API request.
 */
export type MonitorPreviewScenarioId =
  | 'normal'
  | 'high-latency'
  | 'high-error'
  | 'probe-no-traffic'
  | 'quota-abnormal'
  | 'coverage-gap'
  | 'empty'
  | 'loading'
  | 'error'

export type MonitorPreviewRole = 'admin' | 'user'
export type MonitorPreviewViewState = 'ready' | 'empty' | 'loading' | 'error'

export interface MonitorPreviewScenario {
  id: MonitorPreviewScenarioId
  label: string
  description: string
  source: ObservationOverview['source'] | 'none'
  state: MonitorPreviewViewState
  overview: ObservationOverview | null
  stale?: boolean
  errorStatus?: number
}

const HOUR = 60 * 60 * 1000
const BUCKET_HOURS = 4
const WINDOW_MINUTES = 24 * 60
const MINIMUM_SAMPLE = 50
const SNAPSHOT_AT = Date.UTC(2026, 8, 23, 8, 0)

function iso(value: number): string {
  return new Date(value).toISOString()
}

function windowTimes() {
  const end = Math.floor(SNAPSHOT_AT / HOUR) * HOUR
  return { start: end - 24 * HOUR, end }
}

function latency(p50: number | null, p95 = p50 == null ? null : Math.round(p50 * 1.75)): ObservationMetrics['ttft'] {
  return { sample_count: p50 == null ? 0 : 100, p50_ms: p50, p90_ms: p95, p95_ms: p95, avg_ms: p50 }
}

function metrics(overrides: Partial<ObservationMetrics> = {}): ObservationMetrics {
  const requestCount = overrides.request_count ?? 100
  const channelErrors = overrides.channel_errors ?? (requestCount ? 2 : 0)
  const successRequests = overrides.success_requests ?? Math.max(0, requestCount - channelErrors)
  const reliability = overrides.reliability_rate === undefined ? (successRequests + channelErrors ? successRequests / (successRequests + channelErrors) : null) : overrides.reliability_rate
  return {
    success_requests: successRequests,
    channel_errors: channelErrors,
    client_errors: overrides.client_errors ?? 0,
    cancelled_requests: overrides.cancelled_requests ?? 0,
    request_count: requestCount,
    rpm: overrides.rpm ?? requestCount / WINDOW_MINUTES,
    tpm: overrides.tpm ?? requestCount * 820 / WINDOW_MINUTES,
    success_rate: overrides.success_rate === undefined ? reliability : overrides.success_rate,
    reliability_rate: reliability,
    sample_state: overrides.sample_state ?? (requestCount >= MINIMUM_SAMPLE ? 'sufficient' : requestCount > 0 ? 'insufficient' : 'no_samples'),
    cache_rate: overrides.cache_rate === undefined ? (requestCount ? 0.24 : null) : overrides.cache_rate,
    ttft: { ...(overrides.ttft ?? latency(640, 1200)), sample_count: Math.min(successRequests, overrides.ttft?.sample_count ?? successRequests) },
    duration: { ...(overrides.duration ?? latency(3200, 5900)), sample_count: Math.min(successRequests, overrides.duration?.sample_count ?? successRequests) },
    retry_recovered_requests: overrides.retry_recovered_requests ?? (requestCount ? 1 : 0),
    attempt_count: overrides.attempt_count ?? requestCount + channelErrors,
    error_categories: overrides.error_categories,
  }
}

function health(reliability: ObservationHealth['reliability'] = 'healthy', latencyState: ObservationHealth['latency'] = 'healthy'): ObservationHealth {
  return { reliability, latency: latencyState }
}

function bucketSeries(value: ObservationMetrics, bucketHealth: ObservationHealth, withGap = false): ObservationBucket[] {
  const { start } = windowTimes()
  const indices = Array.from({ length: 6 }, (_, index) => index).filter(index => !withGap || index !== 2)
  // Allocate whole events across observed buckets so the visible history
  // reconciles with the channel summary, including incomplete coverage.
  const distribute = (total: number, slot: number) => Math.floor(total * (slot + 1) / indices.length) - Math.floor(total * slot / indices.length)
  return indices.map((index, slot) => {
    const bucketStart = iso(start + index * BUCKET_HOURS * HOUR)
    const successes = distribute(value.success_requests ?? 0, slot)
    const categories = value.error_categories ? Object.fromEntries(Object.entries(value.error_categories).map(([category, count]) => [category, distribute(count, slot)])) : undefined
    const errors = categories ? Object.values(categories).reduce((total, count) => total + count, 0) : distribute(value.channel_errors ?? 0, slot)
    const clients = distribute(value.client_errors ?? 0, slot)
    const cancelled = distribute(value.cancelled_requests ?? 0, slot)
    const requestCount = successes + errors + clients + cancelled
    const reliability = successes + errors ? successes / (successes + errors) : null
    return {
      bucket_start: bucketStart,
      metrics: metrics({
        ...value,
        request_count: requestCount,
        sample_state: requestCount >= MINIMUM_SAMPLE ? 'sufficient' : requestCount > 0 ? 'insufficient' : 'no_samples',
        success_requests: successes,
        channel_errors: errors,
        client_errors: clients,
        cancelled_requests: cancelled,
        reliability_rate: reliability,
        success_rate: requestCount ? successes / requestCount : null,
        rpm: requestCount / (BUCKET_HOURS * 60),
        tpm: requestCount * 820 / (BUCKET_HOURS * 60),
        attempt_count: distribute(value.attempt_count ?? 0, slot),
        retry_recovered_requests: distribute(value.retry_recovered_requests ?? 0, slot),
        ttft: { ...value.ttft, sample_count: Math.min(successes, distribute(value.ttft.sample_count, slot)) },
        duration: { ...value.duration, sample_count: Math.min(successes, distribute(value.duration.sample_count, slot)) },
        error_categories: categories,
      }),
      health: {
        reliability: successes + errors >= MINIMUM_SAMPLE && reliability != null ? reliability >= 0.99 ? 'healthy' : reliability >= 0.95 ? 'warning' : 'critical' : 'unknown',
        latency: Math.min(successes, distribute(value.ttft.sample_count, slot)) >= MINIMUM_SAMPLE ? bucketHealth.latency : 'unknown',
      },
    }
  })
}

function model(name: string, value: ObservationMetrics, modelHealth: ObservationHealth, options: Partial<ObservationModel> = {}): ObservationModel {
  const latencyWarning = modelHealth.reliability === 'healthy' && ['warning', 'critical'].includes(modelHealth.latency)
  return {
    model: name,
    metrics: value,
    traffic: value,
    health: modelHealth,
    current_status: options.current_status ?? { state: latencyWarning ? 'warning' : modelHealth.reliability, source: 'traffic', updated_at: iso(SNAPSHOT_AT - 4 * 60 * 1000), reason: latencyWarning ? 'high_latency' : 'recent_traffic' },
    probe: options.probe,
    quota: options.quota,
    buckets: options.buckets ?? bucketSeries(value, modelHealth),
  }
}

function channel(
  groupId: number,
  groupName: string,
  platform: string,
  value: ObservationMetrics,
  channelHealth: ObservationHealth,
  options: Partial<ObservationChannel> = {},
): ObservationChannel {
  const latencyWarning = channelHealth.reliability === 'healthy' && ['warning', 'critical'].includes(channelHealth.latency)
  const primaryModel = options.models?.[0]?.model || (platform === 'anthropic' ? 'claude-sonnet-4-5' : platform === 'openai' ? 'gpt-5' : 'gemini-2.5-pro')
  return {
    source: options.source,
    platform,
    group_id: groupId,
    group_name: groupName,
    rate_multiplier: options.rate_multiplier ?? 1,
    metrics: value,
    traffic: value,
    health: channelHealth,
    current_status: options.current_status ?? { state: latencyWarning ? 'warning' : channelHealth.reliability, source: 'traffic', updated_at: iso(SNAPSHOT_AT - 4 * 60 * 1000), reason: latencyWarning ? 'high_latency' : 'recent_traffic' },
    probe: options.probe ?? { status: 'healthy', last_checked_at: iso(SNAPSHOT_AT - 8 * 60 * 1000), consecutive_failures: 0 },
    quota: options.quota ?? { status: 'ok', updated_at: iso(SNAPSHOT_AT - 12 * 60 * 1000) },
    buckets: options.buckets ?? bucketSeries(value, channelHealth),
    models: options.models ?? [model(primaryModel, value, channelHealth)],
  }
}

function dimensions(items: ObservationChannel[]): MonitorDimensions {
  const platforms = new Map<string, number>()
  const groups = new Map<number, { id: number; name: string; platform: string; request_count: number }>()
  const models = new Map<string, { value: string; label: string; platform: string; request_count: number }>()
  for (const item of items) {
    const requestCount = item.metrics.request_count ?? 0
    platforms.set(item.platform, (platforms.get(item.platform) || 0) + requestCount)
    groups.set(item.group_id, { id: item.group_id, name: item.group_name, platform: item.platform, request_count: requestCount })
    for (const entry of item.models) {
      const existing = models.get(entry.model)
      models.set(entry.model, { value: entry.model, label: entry.model, platform: item.platform, request_count: (existing?.request_count || 0) + (entry.metrics.request_count ?? 0) })
    }
  }
  return {
    platforms: [...platforms].map(([value, request_count]) => ({ value, label: value[0].toUpperCase() + value.slice(1), request_count })),
    groups: [...groups.values()],
    models: [...models.values()],
  }
}

function coverage(state: ObservationOverview['coverage']['state'] = 'complete', gapReasons: string[] = []): ObservationOverview['coverage'] {
  const { start, end } = windowTimes()
  return {
    requested_start: iso(start),
    requested_end: iso(end),
    coverage_start: iso(state === 'partial' ? start + HOUR : start),
    data_through: iso(state === 'stale' ? end - 2 * HOUR : end),
    computed_at: iso(SNAPSHOT_AT),
    aggregation_lag_seconds: state === 'stale' ? 7200 : state === 'partial' ? 900 : 60,
    coverage_complete: state === 'complete',
    bucket_seconds: BUCKET_HOURS * 3600,
    state,
    detail_retention_hours: 24 * 30,
    unsupported_protocols: state === 'partial' ? ['gemini_stream'] : [],
    collector_state: state === 'stale' ? 'stale' : state === 'partial' ? 'backlogged' : 'healthy',
    pending_events: state === 'partial' ? 42 : 0,
    last_ingested_at: iso(state === 'stale' ? end - 2 * HOUR : end - 60 * 1000),
    gap_reasons: gapReasons,
  }
}

function overview(source: ObservationOverview['source'], items: ObservationChannel[], state: ObservationOverview['coverage']['state'] = 'complete', gapReasons: string[] = []): ObservationOverview {
  return {
    contract_version: 2,
    source,
    mode: 'shadow',
    coverage: coverage(state, gapReasons),
    dimensions: dimensions(items),
    items,
  }
}

const healthyMetrics = metrics()
const slowMetrics = metrics({ request_count: 1200, success_requests: 1194, channel_errors: 6, ttft: latency(12400, 17800), duration: latency(18900, 24000), cache_rate: 0.1 })
const errorMetrics = metrics({ request_count: 100, success_requests: 61, channel_errors: 39, reliability_rate: 0.61, success_rate: 0.61, error_categories: { upstream_5xx: 19, rate_or_capacity: 12, timeout: 8 } })
const noTrafficMetrics = metrics({ request_count: 0, success_requests: 0, channel_errors: 0, reliability_rate: null, success_rate: null, sample_state: 'no_samples', ttft: latency(null), duration: latency(null), rpm: 0, tpm: 0, attempt_count: 0 })
const quotaMetrics = metrics({ request_count: 84, success_requests: 82, channel_errors: 2, reliability_rate: 82 / 84, success_rate: 82 / 84 })

const normalItems = [
  channel(101, 'Claude production', 'anthropic', healthyMetrics, health('healthy', 'healthy'), { source: 'compact' }),
  channel(102, 'OpenAI failover', 'openai', metrics({ request_count: 58, success_requests: 57, channel_errors: 1, reliability_rate: 57 / 58, success_rate: 57 / 58, ttft: latency(790, 1420) }), health('healthy', 'healthy'), { source: 'compact', rate_multiplier: 1.05 }),
]

const slowItems = [
  channel(201, 'Claude slow lane', 'anthropic', slowMetrics, health('healthy', 'critical'), { source: 'compact', models: [model('claude-sonnet-4-5', slowMetrics, health('healthy', 'critical'))] }),
  channel(202, 'Gemini warming', 'gemini', metrics({ ttft: latency(1900, 3300), duration: latency(5000, 8200) }), health('healthy', 'warning'), { source: 'compact' }),
]

const errorItems = [
  channel(301, 'Legacy OpenAI pool', 'openai', errorMetrics, health('critical', 'warning'), { source: 'legacy', probe: { status: 'degraded', last_checked_at: iso(SNAPSHOT_AT - 11 * 60 * 1000), consecutive_failures: 3 }, current_status: { state: 'critical', source: 'traffic', updated_at: iso(SNAPSHOT_AT - 3 * 60 * 1000), reason: 'high_error_rate' }, models: [model('gpt-5', errorMetrics, health('critical', 'warning'))] }),
]

const probeItems = [
  channel(401, 'New probe target', 'anthropic', noTrafficMetrics, health('unknown', 'unknown'), {
    source: 'compact',
    current_status: { state: 'healthy', source: 'probe', updated_at: iso(SNAPSHOT_AT - 2 * 60 * 1000), reason: 'probe_only_no_traffic' },
    probe: { status: 'healthy', last_checked_at: iso(SNAPSHOT_AT - 2 * 60 * 1000), consecutive_failures: 0 },
    quota: { status: 'ok', updated_at: iso(SNAPSHOT_AT - 5 * 60 * 1000) },
    models: [model('claude-sonnet-4-5', noTrafficMetrics, health('unknown', 'unknown'), { current_status: { state: 'healthy', source: 'probe', updated_at: iso(SNAPSHOT_AT - 2 * 60 * 1000), reason: 'probe_only_no_traffic' }, probe: { status: 'healthy', last_checked_at: iso(SNAPSHOT_AT - 2 * 60 * 1000), consecutive_failures: 0 }, quota: { status: 'ok' } })],
  }),
]

const quotaItems = [
  channel(501, 'Quota constrained account', 'gemini', quotaMetrics, health('healthy', 'healthy'), {
    source: 'legacy',
    quota: { status: 'exhausted', updated_at: iso(SNAPSHOT_AT - 3 * 60 * 1000) },
    current_status: { state: 'healthy', source: 'traffic', updated_at: iso(SNAPSHOT_AT - 3 * 60 * 1000), reason: 'recent_traffic' },
  }),
  channel(502, 'Quota sync error', 'openai', healthyMetrics, health('healthy', 'healthy'), {
    source: 'legacy',
    quota: { status: 'error', updated_at: iso(SNAPSHOT_AT - 40 * 60 * 1000) },
    current_status: { state: 'healthy', source: 'traffic', updated_at: iso(SNAPSHOT_AT - 3 * 60 * 1000), reason: 'recent_traffic' },
  }),
]

const coverageItems = [
  channel(601, 'Mixed source history', 'anthropic', healthyMetrics, health('healthy', 'healthy'), { source: 'compact', buckets: bucketSeries(healthyMetrics, health('healthy', 'healthy'), true) }),
  channel(602, 'Legacy backfill', 'openai', errorMetrics, health('warning', 'warning'), { source: 'legacy', buckets: bucketSeries(errorMetrics, health('warning', 'warning'), true) }),
]

export const MONITOR_PREVIEW_SCENARIOS: readonly MonitorPreviewScenario[] = [
  { id: 'normal', label: 'Normal · compact', description: 'Healthy traffic with complete compact coverage over a fixed 24h window.', source: 'compact', state: 'ready', overview: overview('compact', normalItems) },
  { id: 'high-latency', label: 'High latency', description: 'Reliability is healthy while TTFT breaches the critical threshold.', source: 'compact', state: 'ready', overview: overview('compact', slowItems) },
  { id: 'high-error', label: 'High error · legacy', description: 'Legacy traffic has a critical error rate and classified failures.', source: 'legacy', state: 'ready', overview: overview('legacy', errorItems) },
  { id: 'probe-no-traffic', label: 'Probe · no traffic', description: 'An active probe is healthy even though no request samples exist.', source: 'compact', state: 'ready', overview: overview('compact', probeItems) },
  { id: 'quota-abnormal', label: 'Quota abnormal · legacy', description: 'Traffic is healthy but quota evidence is exhausted or stale.', source: 'legacy', state: 'ready', overview: overview('legacy', quotaItems) },
  { id: 'coverage-gap', label: 'Coverage gap · mixed', description: 'Compact and legacy rows coexist with a visible aggregation gap.', source: 'mixed', state: 'ready', overview: overview('mixed', coverageItems, 'partial', ['collector_backlog', 'legacy_window_unavailable', 'unsupported_protocol']) },
  { id: 'empty', label: 'Empty', description: 'The selected range has no channels.', source: 'none', state: 'empty', overview: null },
  { id: 'loading', label: 'Loading', description: 'Initial request is still in flight.', source: 'none', state: 'loading', overview: null },
  { id: 'error', label: 'Error', description: 'The monitor request failed while retaining no snapshot.', source: 'none', state: 'error', overview: null, stale: true, errorStatus: 502 },
]

// Short aliases keep imports convenient in visual checks and tests.
export const monitorPreviewScenarios = MONITOR_PREVIEW_SCENARIOS
export const MONITOR_PREVIEW_FIXTURES = MONITOR_PREVIEW_SCENARIOS

// Match public API redaction: retain health/rates while removing operator counters.
function redactMetrics(value: ObservationMetrics): void {
  for (const field of ['success_requests', 'channel_errors', 'client_errors', 'cancelled_requests', 'unknown_requests', 'unclassified_attempts', 'request_count', 'attempt_count', 'retry_recovered_requests', 'rpm', 'tpm'] as const) delete value[field]
  delete value.error_categories
  delete value.phase_avg_ms
  value.ttft.sample_count = 0
  value.duration.sample_count = 0
}

export function getMonitorPreviewScenario(id: MonitorPreviewScenarioId, role: MonitorPreviewRole = 'admin'): MonitorPreviewScenario {
  const scenario = MONITOR_PREVIEW_SCENARIOS.find(item => item.id === id) || MONITOR_PREVIEW_SCENARIOS[0]
  const copy = JSON.parse(JSON.stringify(scenario)) as MonitorPreviewScenario
  if (role === 'user' && copy.overview) {
    for (const item of copy.overview.items) {
      redactMetrics(item.metrics)
      if (item.traffic) redactMetrics(item.traffic)
      item.buckets.forEach(bucket => redactMetrics(bucket.metrics))
      for (const entry of item.models) {
        redactMetrics(entry.metrics)
        if (entry.traffic) redactMetrics(entry.traffic)
        entry.buckets.forEach(bucket => redactMetrics(bucket.metrics))
      }
    }
    copy.overview.dimensions.platforms.forEach(item => { item.request_count = 0 })
    copy.overview.dimensions.groups.forEach(item => { item.request_count = 0 })
    copy.overview.dimensions.models.forEach(item => { item.request_count = 0 })
    copy.overview.coverage.gap_reasons = []
    copy.overview.coverage.pending_events = 0
    copy.overview.coverage.unsupported_protocols = []
  }
  return copy
}
