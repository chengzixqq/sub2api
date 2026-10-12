import type {
  MonitorDimensions,
  MonitorRange,
  ObservationBucket,
  ObservationChannel,
  ObservationHealth,
  ObservationMetrics,
  ObservationModel,
  ObservationOverview,
} from '@/api/channelMonitorV2'
import { MONITOR_RANGE_MINUTES, resolveMonitorHistory } from './monitorHistoryPreferences'

/**
 * Local-only monitoring fixtures. They deliberately model the response
 * contract used by the real monitor so visual checks do not need a server,
 * credentials, or an API request.
 */
export type MonitorPreviewScenarioId =
  | 'normal'
  | 'card-gallery'
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

function windowTimes(range: MonitorRange = '24h') {
  const end = Math.floor(SNAPSHOT_AT / HOUR) * HOUR
  return { start: end - MONITOR_RANGE_MINUTES[range] * 60000, end }
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

function bucketSeries(value: ObservationMetrics, bucketHealth: ObservationHealth, withGap = false, count = 6, bucketMinutes = 24 * 60 / count, observed?: Array<{ start: number; end: number }>, timeWindow?: ReturnType<typeof windowTimes>): ObservationBucket[] {
  const { start, end } = timeWindow || windowTimes()
  const step = bucketMinutes * 60 * 1000
  const slots: Array<{ start: number; sampleStart: number; sampleEnd: number; minutes: number; weight: number }> = []
  for (let at = Math.floor(start / step) * step, index = 0; at < end; at += step, index++) {
    if (withGap && index === 2) continue
    const from = Math.max(start, at)
    const to = Math.min(end, at + step)
    const spans = observed ? observed.map(span => ({ start: Math.max(from, span.start), end: Math.min(to, span.end) })).filter(span => span.end > span.start) : [{ start: from, end: to }]
    const weight = spans.reduce((sum, span) => sum + span.end - span.start, 0)
    if (weight > 0) slots.push({ start: at, sampleStart: timeWindow ? Math.min(...spans.map(span => span.start)) : from, sampleEnd: timeWindow ? Math.max(...spans.map(span => span.end)) : to, minutes: (to - from) / 60000, weight })
  }
  // These are fictional event samples, not resampled production percentiles.
  // Allocate whole samples by observed time; preserve gaps and all event totals.
  const totalWeight = slots.reduce((sum, slot) => sum + slot.weight, 0)
  let through = 0
  return slots.map(slot => {
    const before = through
    through += slot.weight
    const distribute = (total: number) => Math.floor(total * through / totalWeight) - Math.floor(total * before / totalWeight)
    const bucketStart = iso(slot.start)
    const successes = distribute(value.success_requests ?? 0)
    // In explicit range fixtures, latency/retry samples belong to successful
    // events. Independent rounding followed by min() would silently lose them.
    const distributeSuccesses = (total: number) => {
      const population = value.success_requests ?? 0
      if (!population) return 0
      const eligible = Math.min(total, population)
      const successfulBefore = Math.floor(population * before / totalWeight)
      return Math.floor(eligible * (successfulBefore + successes) / population) - Math.floor(eligible * successfulBefore / population)
    }
    const ttftSamples = timeWindow ? distributeSuccesses(value.ttft.sample_count) : Math.min(successes, distribute(value.ttft.sample_count))
    const durationSamples = timeWindow ? distributeSuccesses(value.duration.sample_count) : Math.min(successes, distribute(value.duration.sample_count))
    const categories = value.error_categories ? Object.fromEntries(Object.entries(value.error_categories).map(([category, count]) => [category, distribute(count)])) : undefined
    const errors = categories ? Object.values(categories).reduce((total, count) => total + count, 0) : distribute(value.channel_errors ?? 0)
    const clients = distribute(value.client_errors ?? 0)
    const cancelled = distribute(value.cancelled_requests ?? 0)
    const requestCount = successes + errors + clients + cancelled
    const reliability = successes + errors ? successes / (successes + errors) : null
    return {
      bucket_start: bucketStart,
      sample_start: iso(slot.sampleStart),
      sample_end: iso(slot.sampleEnd),
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
        rpm: requestCount / slot.minutes,
        tpm: requestCount * 820 / slot.minutes,
        attempt_count: timeWindow ? requestCount + distribute(Math.max(0, (value.attempt_count ?? 0) - (value.request_count ?? 0))) : distribute(value.attempt_count ?? 0),
        retry_recovered_requests: timeWindow ? distributeSuccesses(value.retry_recovered_requests ?? 0) : distribute(value.retry_recovered_requests ?? 0),
        ttft: { ...value.ttft, sample_count: ttftSamples },
        duration: { ...value.duration, sample_count: durationSamples },
        error_categories: categories,
      }),
      health: {
        reliability: successes + errors >= MINIMUM_SAMPLE && reliability != null ? reliability >= 0.99 ? 'healthy' : reliability >= 0.95 ? 'warning' : 'critical' : 'unknown',
        latency: ttftSamples >= MINIMUM_SAMPLE ? bucketHealth.latency : 'unknown',
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

// A denser local fixture exercises the real two/three-column card layout.
const galleryItems = [
  channel(801, 'Claude Standard', 'anthropic', metrics({ request_count: 6000, channel_errors: 18, cache_rate: .634, ttft: latency(3870) }), health(), { source: 'compact', rate_multiplier: 1.3 }),
  channel(802, 'Claude Backup', 'anthropic', metrics({ request_count: 6000, channel_errors: 120, cache_rate: .56, ttft: latency(5630) }), health('warning'), { source: 'compact', rate_multiplier: .2 }),
  channel(803, 'ChatGPT Standard', 'openai', metrics({ request_count: 6000, channel_errors: 24, cache_rate: .868, ttft: latency(4360) }), health(), { source: 'compact', rate_multiplier: .15 }),
  channel(804, 'ChatGPT Priority', 'openai', metrics({ request_count: 6000, channel_errors: 6, cache_rate: .733, ttft: latency(2700) }), health(), { source: 'compact', rate_multiplier: .25 }),
  channel(805, 'ChatGPT Backup', 'openai', metrics({ request_count: 6000, channel_errors: 900, cache_rate: .945, ttft: latency(8630) }), health('critical'), { source: 'compact', rate_multiplier: .04, probe: { status: 'error', consecutive_failures: 2 } }),
]

for (const item of galleryItems) {
  item.buckets = bucketSeries(item.metrics, item.health, false, 18)
  for (const entry of item.models) entry.buckets = bucketSeries(entry.metrics, entry.health, false, 18)
}
const galleryOverview = overview('compact', galleryItems)
galleryOverview.coverage.bucket_seconds = 24 * 3600 / 18

export const MONITOR_PREVIEW_SCENARIOS: readonly MonitorPreviewScenario[] = [
  { id: 'card-gallery', label: 'Card gallery', description: 'Five fictional channels for responsive visual review; no live data.', source: 'compact', state: 'ready', overview: galleryOverview },
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

/**
 * Each explicit range is an independent fictional event window. Template totals
 * are allocated as whole samples over that window, with template gaps preserved
 * at their relative positions. This is not a projection of historical live data.
 */
function rebuildRange(value: ObservationOverview, range: MonitorRange, bucketMinutes?: number): void {
  const target = windowTimes(range)
  const sourceStart = Date.parse(value.coverage.requested_start)
  const sourceEnd = Date.parse(value.coverage.requested_end || value.coverage.data_through)
  const sourceStep = value.coverage.bucket_seconds * 1000
  const minutes = MONITOR_RANGE_MINUTES[range]
  const duration = target.end - target.start
  const moveTime = (at: number) => target.start + (at - sourceStart) * duration / (sourceEnd - sourceStart)
  const interval = resolveMonitorHistory(range, {
    historyMode: bucketMinutes === undefined ? 'auto' : 'interval',
    historyBucketMinutes: bucketMinutes ?? 5,
    historyBarCount: 60,
  }).bucketMinutes
  const setRates = (metrics: ObservationMetrics) => {
    metrics.rpm = (metrics.request_count ?? 0) / minutes
    metrics.tpm = (metrics.request_count ?? 0) * 820 / minutes
  }
  const replace = (row: ObservationChannel | ObservationModel) => {
    const observed = row.buckets.map(bucket => ({
      start: moveTime(Date.parse(bucket.sample_start || bucket.bucket_start)),
      end: moveTime(Date.parse(bucket.sample_end || iso(Date.parse(bucket.bucket_start) + sourceStep))),
    }))
    setRates(row.metrics)
    if (row.traffic) setRates(row.traffic)
    row.buckets = bucketSeries(row.metrics, row.health, false, 6, interval, observed, target)
  }
  for (const item of value.items) {
    replace(item)
    item.models.forEach(replace)
  }
  const boundaries = value.items.flatMap(item => [item, ...item.models]).flatMap(row => row.buckets)
  value.coverage = {
    ...value.coverage,
    requested_start: iso(target.start),
    requested_end: iso(target.end),
    coverage_start: iso(boundaries.length ? Math.min(...boundaries.map(bucket => Date.parse(bucket.sample_start!))) : target.start),
    data_through: iso(boundaries.length ? Math.max(...boundaries.map(bucket => Date.parse(bucket.sample_end!))) : target.end),
    bucket_seconds: interval * 60,
  }
  value.dimensions = dimensions(value.items)
}

export function getMonitorPreviewScenario(id: MonitorPreviewScenarioId, role: MonitorPreviewRole = 'admin', bucketMinutes?: number, range?: MonitorRange): MonitorPreviewScenario {
  const scenario = MONITOR_PREVIEW_SCENARIOS.find(item => item.id === id) || MONITOR_PREVIEW_SCENARIOS.find(item => item.id === 'normal')!
  const copy = JSON.parse(JSON.stringify(scenario)) as MonitorPreviewScenario
  if (copy.overview && range !== undefined) {
    rebuildRange(copy.overview, range, bucketMinutes)
    copy.description = `Fictional ${range} event samples for ${copy.label}; no live data or API requests.`
  } else if (copy.overview && bucketMinutes !== undefined && Number.isInteger(bucketMinutes) && bucketMinutes >= 1 && bucketMinutes <= WINDOW_MINUTES) {
    const sourceStep = copy.overview.coverage.bucket_seconds * 1000
    const replaceBuckets = (item: ObservationChannel | ObservationModel) => {
      const observed = item.buckets.map(bucket => ({ start: Date.parse(bucket.bucket_start), end: Date.parse(bucket.bucket_start) + sourceStep }))
      item.buckets = bucketSeries(item.metrics, item.health, false, 6, bucketMinutes, observed)
    }
    for (const item of copy.overview.items) {
      replaceBuckets(item)
      item.models.forEach(replaceBuckets)
    }
    copy.overview.coverage.bucket_seconds = bucketMinutes * 60
  }
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
