import type { ObservationChannel, ObservationOverview } from '@/api/channelMonitorV2'

/** Quantiles of channels, never a fabricated request-level aggregate percentile. */
export function median(values: Array<number | null | undefined>): number | null {
  const sorted = values.filter((value): value is number => value != null && Number.isFinite(value)).sort((a, b) => a - b)
  const mid = Math.floor(sorted.length / 2)
  return sorted.length ? sorted.length % 2 ? sorted[mid] : (sorted[mid - 1] + sorted[mid]) / 2 : null
}

export function summarizeMonitorTraffic(overview: ObservationOverview | null) {
  const items = overview?.items || []
  const available = overview?.coverage.state !== 'unavailable'
  const sufficient = available ? items.filter(item => (item.traffic || item.metrics).sample_state === 'sufficient') : []
  const healthy = sufficient.filter(item => item.health.reliability === 'healthy' && item.health.latency === 'healthy').length
  const bySource = new Map<string, ObservationChannel[]>()
  for (const item of sufficient) {
    const source = item.source || overview?.source || 'unknown'
    const channels = bySource.get(source) || []
    channels.push(item)
    bySource.set(source, channels)
  }
  // User projections intentionally zero operational request counters. Do not
  // weight by those counters, or combine sources with different semantics.
  const sources = [...bySource].sort(([a], [b]) => a.localeCompare(b)).map(([source, channels]) => ({
    source,
    ttftP50: median(channels.map(item => (item.traffic || item.metrics).ttft?.p50_ms)),
    errorRate: median(channels.map(item => {
      const rate = (item.traffic || item.metrics).reliability_rate
      return rate == null ? null : 1 - rate
    })),
  }))
  return { total: items.length, sufficient: sufficient.length, healthy, healthRate: sufficient.length ? healthy / sufficient.length : null, sources }
}
