import { GROUP_PLATFORM_OPTIONS } from '@/constants/platforms'
import type { ObservationBucket, ObservationChannel, ObservationMetrics, ObservationOverview } from '@/api/channelMonitorV2'

export type ObservationLayout = 'cards' | 'matrix'
export type ObservationSort = 'default' | 'reliability' | 'latency'
export function observationPreferenceKey(userId: number | undefined, role: string, workspaceId: number | undefined) {
  return `sub2api:observation:v2:${userId ?? 'anonymous'}:${role}:${workspaceId ?? 'owner'}`
}
export function observationSections(items: ObservationChannel[], sort: ObservationSort = 'default') {
  const groups = new Map<string, ObservationChannel[]>()
  for (const item of items) {
    const rows = groups.get(item.platform) || []
    rows.push(item)
    groups.set(item.platform, rows)
  }
  const order = GROUP_PLATFORM_OPTIONS.map(p => p.value as string)
  const position = (platform: string) => order.includes(platform) ? order.indexOf(platform) : order.length
  return [...groups].sort(([a], [b]) => position(a) - position(b) || a.localeCompare(b)).map(([platform, rows]) => ({
    platform,
    items: [...rows].sort((a, b) => {
      const difference = sort === 'reliability'
        ? (b.metrics.reliability_rate ?? -1) - (a.metrics.reliability_rate ?? -1)
        : sort === 'latency' ? (a.metrics.ttft.p50_ms ?? Infinity) - (b.metrics.ttft.p50_ms ?? Infinity) : 0
      return difference || a.group_name.localeCompare(b.group_name) || a.group_id - b.group_id
    }),
  }))
}
export function observationStatus(metrics: ObservationMetrics, coverage: ObservationOverview['coverage']['state']) {
  return coverage === 'complete' ? metrics.sample_state : coverage
}
export function observationTimeline(buckets: ObservationBucket[], coverage: ObservationOverview['coverage']) {
  const start = Date.parse(coverage.requested_start)
  const end = Date.parse(coverage.requested_end || coverage.data_through)
  const step = coverage.bucket_seconds * 1000
  if (!Number.isFinite(start) || !Number.isFinite(end) || step <= 0) return []
  const indexed = new Map(buckets.map(b => [Date.parse(b.bucket_start), b]))
  const result: Array<{ start: string; bucket: ObservationBucket | null }> = []
  for (let at = Math.floor(start / step) * step; at < end && result.length < 1500; at += step) {
    result.push({ start: new Date(at).toISOString(), bucket: indexed.get(at) ?? null })
  }
  return result
}
