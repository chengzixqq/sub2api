import { effectScope, ref } from 'vue'
import { describe, expect, it } from 'vitest'
import type { MonitorFilter, MonitorRange } from '@/api/channelMonitorV2'
import { resolveMonitorHistory } from '../monitorHistoryPreferences'
import { DEFAULT_MONITOR_PREFERENCES, normalizeMonitorPreferences } from '../useMonitorPreferences'
import { useMonitorHistoryFilter } from '../useMonitorHistoryFilter'

describe('history aggregation preferences', () => {
  it.each<MonitorRange>(['90m', '24h', '7d', '30d'])('keeps auto defaults and sends no override for %s', range => {
    const result = resolveMonitorHistory(range, DEFAULT_MONITOR_PREFERENCES)
    expect(result.params).toEqual({})
    expect(result.bucketMinutes).toBe(['7d', '30d'].includes(range) ? 60 : 5)
  })

  it.each([
    ['24h', 1, 1], ['24h', 7, 7], ['24h', 30, 30], ['90m', 180, 90], ['7d', 35, 60], ['30d', 65, 120], ['7d', 43200, 10080],
  ] as const)('resolves interval %s / %i to %i minutes without rewriting the preference', (range, minutes, actual) => {
    const preferences = { ...DEFAULT_MONITOR_PREFERENCES, historyMode: 'interval' as const, historyBucketMinutes: minutes }
    const result = resolveMonitorHistory(range, preferences)
    expect(result.params).toEqual({ bucket_minutes: actual })
    expect(result.bucketMinutes).toBe(actual)
    expect(result.adjusted).toBe(minutes !== actual)
    expect(preferences.historyBucketMinutes).toBe(minutes)
  })

  it.each([
    ['24h', 60, 24], ['24h', 720, 2], ['90m', 1, 90], ['90m', 720, 1], ['7d', 60, 180], ['30d', 720, 60], ['30d', 1, 43200],
  ] as const)('uses the server count formula for %s / %i bars', (range, count, minutes) => {
    const result = resolveMonitorHistory(range, { ...DEFAULT_MONITOR_PREFERENCES, historyMode: 'count', historyBarCount: count })
    expect(result.params).toEqual({ bucket_count: count })
    expect(result.bucketMinutes).toBe(minutes)
  })

  it('rejects malformed storage, preserves valid values and migrates old preferences to auto', () => {
    expect(normalizeMonitorPreferences({ density: 'comfortable' })).toEqual({ ...DEFAULT_MONITOR_PREFERENCES, density: 'comfortable' })
    expect(normalizeMonitorPreferences({ historyMode: 'loop', historyBucketMinutes: '30', historyBarCount: 721 })).toEqual(DEFAULT_MONITOR_PREFERENCES)
    expect(normalizeMonitorPreferences({ historyMode: 'interval', historyBucketMinutes: 35, historyBarCount: 70 })).toMatchObject({ historyMode: 'interval', historyBucketMinutes: 35, historyBarCount: 70 })
    for (const value of [0, -1, 5.5, 43205, Infinity, NaN]) expect(normalizeMonitorPreferences({ historyBucketMinutes: value }).historyBucketMinutes).toBe(5)
    for (const value of [0, -1, 1.1, 721, Infinity, NaN]) expect(normalizeMonitorPreferences({ historyBarCount: value }).historyBarCount).toBe(60)
  })

  it('keeps request modes mutually exclusive on range changes, resets and auto mode', () => {
    const scope = effectScope()
    const filter = ref<MonitorFilter>({ range: '24h', platforms: [], groupIds: [], models: [] })
    const preferences = ref({ ...DEFAULT_MONITOR_PREFERENCES, historyMode: 'interval' as 'interval' | 'count' | 'auto', historyBucketMinutes: 35 })
    scope.run(() => useMonitorHistoryFilter(filter, preferences))
    expect(filter.value.bucket_minutes).toBe(35)
    filter.value.range = '7d'
    expect(filter.value.bucket_minutes).toBe(60)
    expect(preferences.value.historyBucketMinutes).toBe(35)
    filter.value.range = '24h'
    expect(filter.value.bucket_minutes).toBe(35)
    preferences.value.historyMode = 'count'
    expect(filter.value.bucket_count).toBe(60)
    expect(filter.value).not.toHaveProperty('bucket_minutes')
    filter.value = { range: '24h', platforms: [], groupIds: [], models: [] }
    expect(filter.value.bucket_count).toBe(60)
    preferences.value.historyMode = 'auto'
    expect(filter.value).not.toHaveProperty('bucket_count')
    expect(filter.value).not.toHaveProperty('bucket_minutes')
    scope.stop()
  })
})
