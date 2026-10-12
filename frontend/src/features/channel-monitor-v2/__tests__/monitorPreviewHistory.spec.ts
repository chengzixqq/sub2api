import { describe, expect, it } from 'vitest'
import { getMonitorPreviewScenario } from '../monitorPreview'
import { observationTimeline } from '../observationViewModel'

describe('preview history sample windows', () => {
  it('keeps the original gallery when no history option is selected', () => {
    const overview = getMonitorPreviewScenario('card-gallery').overview!
    expect(overview.coverage.bucket_seconds).toBe(80 * 60)
    expect(overview.items[0]!.buckets).toHaveLength(18)
  })

  it.each([[30, 48], [60, 24]])('uses %i-minute sample windows across the complete day (%i bars)', (minutes, count) => {
    const overview = getMonitorPreviewScenario('card-gallery', 'admin', minutes).overview!
    expect(overview.coverage.bucket_seconds).toBe(minutes * 60)
    for (const item of overview.items) {
      expect(item.buckets).toHaveLength(count)
      expect(observationTimeline(item.buckets, overview.coverage)).toHaveLength(count)
      for (const row of [item, ...item.models]) {
        for (const field of ['request_count', 'success_requests', 'channel_errors', 'attempt_count'] as const) {
          expect(row.buckets.reduce((sum, bucket) => sum + (bucket.metrics[field] ?? 0), 0)).toBe(row.metrics[field])
        }
      }
    }
    // Rebuilding the fixture must not mutate the shared scenario or totals.
    expect(overview.items[0]!.metrics).toEqual(getMonitorPreviewScenario('card-gallery').overview!.items[0]!.metrics)
    expect(getMonitorPreviewScenario('card-gallery').overview!.coverage.bucket_seconds).toBe(80 * 60)
  })

  it('aligns arbitrary supported periods to Unix time without dropping partial edge samples', () => {
    const overview = getMonitorPreviewScenario('card-gallery', 'admin', 35).overview!
    const row = overview.items[0]!
    const slots = observationTimeline(row.buckets, overview.coverage)
    expect(slots.every(slot => slot.bucket !== null)).toBe(true)
    expect(row.buckets.every(bucket => Date.parse(bucket.bucket_start) % (35 * 60000) === 0)).toBe(true)
    expect(row.buckets.reduce((sum, bucket) => sum + bucket.metrics.request_count!, 0)).toBe(row.metrics.request_count)
    expect(Date.parse(slots[0]!.start)).toBeLessThanOrEqual(Date.parse(overview.coverage.requested_start))
    expect(Date.parse(slots.at(-1)!.start) + 35 * 60000).toBeGreaterThanOrEqual(Date.parse(overview.coverage.requested_end))
    expect(row.buckets[0]!.sample_start).toBe(overview.coverage.requested_start)
    expect(row.buckets.at(-1)!.sample_end).toBe(overview.coverage.requested_end)
  })

  it('supports true minute intervals instead of restricting them to the previous five-minute default', () => {
    const overview = getMonitorPreviewScenario('card-gallery', 'admin', 1).overview!
    expect(overview.coverage.bucket_seconds).toBe(60)
    expect(overview.items[0]!.buckets).toHaveLength(1440)
    expect(overview.items[0]!.buckets.reduce((sum, bucket) => sum + bucket.metrics.request_count!, 0)).toBe(6000)
    const uneven = getMonitorPreviewScenario('card-gallery', 'admin', 7).overview!
    expect(uneven.coverage.bucket_seconds).toBe(420)
    expect(observationTimeline(uneven.items[0]!.buckets, uneven.coverage).every(slot => slot.bucket !== null)).toBe(true)
  })

  it('keeps missing history visibly missing and does not turn no-traffic into green samples', () => {
    const gap = getMonitorPreviewScenario('coverage-gap', 'admin', 30).overview!
    expect(gap.coverage.state).toBe('partial')
    const row = gap.items[0]!
    expect(observationTimeline(row.buckets, gap.coverage).some(slot => !slot.bucket)).toBe(true)
    expect(row.buckets.reduce((sum, bucket) => sum + bucket.metrics.request_count!, 0)).toBe(row.metrics.request_count)
    const noTraffic = getMonitorPreviewScenario('probe-no-traffic', 'admin', 30).overview!.items[0]!
    expect(noTraffic.buckets.every(bucket => bucket.metrics.request_count === 0 && bucket.health.reliability === 'unknown')).toBe(true)
  })

  it('retains error samples, error categories and operator redaction after rebuilding buckets', () => {
    const row = getMonitorPreviewScenario('high-error', 'admin', 60).overview!.items[0]!
    expect(row.buckets.reduce((sum, bucket) => sum + bucket.metrics.channel_errors!, 0)).toBe(39)
    for (const [category, total] of Object.entries(row.metrics.error_categories!)) {
      expect(row.buckets.reduce((sum, bucket) => sum + bucket.metrics.error_categories![category]!, 0)).toBe(total)
    }
    const publicRow = getMonitorPreviewScenario('card-gallery', 'user', 30).overview!.items[0]!
    for (const bucket of [...publicRow.buckets, ...publicRow.models.flatMap(model => model.buckets)]) {
      expect(bucket.metrics.request_count).toBeUndefined()
      expect(bucket.metrics.channel_errors).toBeUndefined()
      expect(bucket.metrics.ttft.sample_count).toBe(0)
    }
  })
})
