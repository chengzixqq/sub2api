import { describe, expect, it } from 'vitest'
import { observationSections, observationStatus, observationPreferenceKey, observationTimeline } from '../observationViewModel'
import type { ObservationChannel, ObservationOverview } from '@/api/channelMonitorV2'

function channel(platform: string, name: string, id: number): ObservationChannel {
  return { platform, group_name: name, group_id: id, models: [], buckets: [], metrics: { request_count: 0, sample_state: 'sufficient', reliability_rate: 0.99 } as ObservationChannel['metrics'], health: { reliability: 'healthy', latency: 'critical' } }
}

describe('observation view model', () => {
  it('uses explicit platform IDs and stable names, never infers a platform from a group name', () => {
    const input = [channel('composite', 'OpenAI multi', 4), channel('openai', 'Z', 3), channel('anthropic', 'A', 2), channel('openai', 'A', 1)]
    expect(observationSections(input).map(s => [s.platform, s.items.map(i => i.group_id)])).toEqual([['anthropic', [2]], ['openai', [1, 3]], ['composite', [4]]])
  })
  it('honors redacted sample state instead of reading zero private counters as no traffic', () => {
    expect(observationStatus(channel('openai', 'A', 1).metrics, 'complete')).toBe('sufficient')
    expect(observationStatus(channel('openai', 'A', 1).metrics, 'stale')).toBe('stale')
    expect(observationStatus(channel('openai', 'A', 1).metrics, 'partial')).toBe('partial')
  })
  it('isolates preferences by user, role and workspace', () => {
    expect(observationPreferenceKey(1, 'admin', 3)).not.toBe(observationPreferenceKey(1, 'user', 3))
    expect(observationPreferenceKey(1, 'user', 3)).not.toBe(observationPreferenceKey(2, 'user', 3))
    expect(observationPreferenceKey(1, 'user', 3)).not.toBe(observationPreferenceKey(1, 'user', 4))
  })
  it('fills missing time buckets without manufacturing healthy samples', () => {
    const coverage = { requested_start: '2026-09-15T00:00:00Z', requested_end: '2026-09-15T00:03:00Z', bucket_seconds: 60 } as ObservationOverview['coverage']
    expect(observationTimeline([], coverage)).toEqual([
      { start: '2026-09-15T00:00:00.000Z', bucket: null },
      { start: '2026-09-15T00:01:00.000Z', bucket: null },
      { start: '2026-09-15T00:02:00.000Z', bucket: null },
    ])
  })
})
