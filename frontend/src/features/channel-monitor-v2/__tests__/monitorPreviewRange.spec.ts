import { describe, expect, it } from 'vitest'
import type { MonitorRange, ObservationChannel, ObservationModel } from '@/api/channelMonitorV2'
import { getMonitorPreviewScenario, MONITOR_PREVIEW_SCENARIOS } from '../monitorPreview'
import { MONITOR_RANGE_MINUTES } from '../monitorHistoryPreferences'
import { observationTimeline } from '../observationViewModel'

const ranges: MonitorRange[] = ['90m', '24h', '7d', '30d']
const sampleFields = ['request_count', 'success_requests', 'channel_errors', 'client_errors', 'cancelled_requests', 'attempt_count', 'retry_recovered_requests'] as const
function expectConserved(row: ObservationChannel | ObservationModel, minutes: number) {
  for (const field of sampleFields) {
    expect(row.buckets.every(bucket => Number.isInteger(bucket.metrics[field]))).toBe(true)
    expect(row.buckets.reduce((sum, bucket) => sum + (bucket.metrics[field] ?? 0), 0)).toBe(row.metrics[field])
  }
  expect(row.metrics.rpm).toBeCloseTo(row.metrics.request_count! / minutes)
  expect(row.traffic?.rpm).toBe(row.metrics.rpm)
  for (const [category, count] of Object.entries(row.metrics.error_categories || {})) {
    expect(row.buckets.reduce((sum, bucket) => sum + (bucket.metrics.error_categories?.[category] || 0), 0)).toBe(count)
  }
}

describe('range-aware local monitor fixtures', () => {
  it.each(ranges)('creates a complete %s window with real default grain and model histories', range => {
    const result = getMonitorPreviewScenario('card-gallery', 'admin', undefined, range).overview!
    const minutes = MONITOR_RANGE_MINUTES[range]
    const stepSeconds = range === '7d' || range === '30d' ? 3600 : 300
    const start = Date.parse(result.coverage.requested_start)
    const end = Date.parse(result.coverage.requested_end)
    expect((end - start) / 60000).toBe(minutes)
    expect(result.coverage.bucket_seconds).toBe(stepSeconds)
    expect(result.coverage.coverage_start).toBe(result.coverage.requested_start)
    expect(Date.parse(result.coverage.data_through)).toBeLessThanOrEqual(end)
    for (const item of result.items) for (const row of [item, ...item.models]) {
      expectConserved(row, minutes)
      const slots = observationTimeline(row.buckets, result.coverage)
      expect(slots).toHaveLength(minutes * 60 / stepSeconds)
      expect(slots.every(slot => slot.bucket !== null)).toBe(true)
      for (const bucket of row.buckets) {
        const at = Date.parse(bucket.bucket_start)
        expect(at % (stepSeconds * 1000)).toBe(0)
        expect(Date.parse(bucket.sample_start!)).toBe(Math.max(start, at))
        expect(Date.parse(bucket.sample_end!)).toBe(Math.min(end, at + stepSeconds * 1000))
      }
    }
  })

  it.each([['90m', 7], ['24h', 35], ['7d', 180], ['30d', 360]] as const)('applies a %s / %i minute interval without clipping partial edges', (range, bucketMinutes) => {
    const result = getMonitorPreviewScenario('high-error', 'admin', bucketMinutes, range).overview!
    expect(result.coverage.bucket_seconds).toBe(bucketMinutes * 60)
    expect(Date.parse(result.coverage.requested_end) - Date.parse(result.coverage.requested_start)).toBe(MONITOR_RANGE_MINUTES[range] * 60000)
    for (const item of result.items) for (const row of [item, ...item.models]) {
      expectConserved(row, MONITOR_RANGE_MINUTES[range])
      const slots = observationTimeline(row.buckets, result.coverage)
      expect(slots.every(slot => slot.bucket !== null)).toBe(true)
      expect(row.buckets[0]!.sample_start).toBe(result.coverage.requested_start)
      expect(row.buckets.at(-1)!.sample_end).toBe(result.coverage.requested_end)
    }
  })

  it.each(ranges)('preserves gaps, source state and zero-traffic evidence for %s', range => {
    const gap = getMonitorPreviewScenario('coverage-gap', 'admin', undefined, range).overview!
    expect(gap.source).toBe('mixed')
    expect(gap.coverage.state).toBe('partial')
    expect(gap.coverage.coverage_complete).toBe(false)
    expect(gap.coverage.gap_reasons?.length).toBeGreaterThan(0)
    for (const item of gap.items) {
      expect(observationTimeline(item.buckets, gap.coverage).some(slot => !slot.bucket)).toBe(true)
      expectConserved(item, MONITOR_RANGE_MINUTES[range])
    }
    const noTraffic = getMonitorPreviewScenario('probe-no-traffic', 'admin', undefined, range).overview!
    for (const item of noTraffic.items) for (const row of [item, ...item.models]) {
      expect(row.metrics.request_count).toBe(0)
      expect(row.buckets.length).toBeGreaterThan(0)
      expect(row.buckets.every(bucket => bucket.metrics.request_count === 0 && bucket.health.reliability === 'unknown' && bucket.metrics.sample_state === 'no_samples')).toBe(true)
    }
  })

  it.each(ranges)('retains user redaction and non-ready states for %s', range => {
    const result = getMonitorPreviewScenario('coverage-gap', 'user', undefined, range).overview!
    expect(result.coverage.gap_reasons).toEqual([])
    expect(result.coverage.pending_events).toBe(0)
    for (const item of result.items) for (const row of [item, ...item.models]) {
      for (const metrics of [row.metrics, row.traffic!, ...row.buckets.map(bucket => bucket.metrics)]) {
        expect(metrics.request_count).toBeUndefined()
        expect(metrics.channel_errors).toBeUndefined()
        expect(metrics.error_categories).toBeUndefined()
        expect(metrics.ttft.sample_count).toBe(0)
      }
    }
    for (const state of ['loading', 'empty', 'error'] as const) {
      const scenario = getMonitorPreviewScenario(state, 'user', undefined, range)
      expect(scenario.state).toBe(state)
      expect(scenario.overview).toBeNull()
    }
  })

  it('conserves latency samples and keeps attempts above requests in sparse minute buckets', () => {
    const result = getMonitorPreviewScenario('high-latency', 'admin', 1, '24h').overview!
    for (const item of result.items) for (const row of [item, ...item.models]) {
      expectConserved(row, 1440)
      for (const field of ['ttft', 'duration'] as const) {
        expect(row.buckets.reduce((sum, bucket) => sum + bucket.metrics[field].sample_count, 0)).toBe(row.metrics[field].sample_count)
      }
      for (const bucket of row.buckets) {
        expect(bucket.metrics.attempt_count!).toBeGreaterThanOrEqual(bucket.metrics.request_count!)
        expect(bucket.metrics.ttft.sample_count).toBeLessThanOrEqual(bucket.metrics.success_requests!)
        expect(bucket.metrics.duration.sample_count).toBeLessThanOrEqual(bucket.metrics.success_requests!)
      }
    }
  })

  it('keeps old callers unchanged and never mutates shared fixture state', () => {
    const before = JSON.stringify(MONITOR_PREVIEW_SCENARIOS)
    const old = getMonitorPreviewScenario('card-gallery')
    for (const range of ranges) getMonitorPreviewScenario('card-gallery', 'admin', undefined, range)
    expect(JSON.stringify(MONITOR_PREVIEW_SCENARIOS)).toBe(before)
    expect(getMonitorPreviewScenario('card-gallery')).toEqual(old)
    expect(old.overview?.coverage.bucket_seconds).toBe(80 * 60)
    expect(old.overview?.items[0].buckets).toHaveLength(18)
  })
})
