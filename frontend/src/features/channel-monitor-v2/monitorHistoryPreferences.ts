import type { MonitorRange } from '@/api/channelMonitorV2'

export interface MonitorHistoryPreferences {
  historyMode: 'auto' | 'interval' | 'count'
  historyBucketMinutes: number
  historyBarCount: number
}

export const MONITOR_RANGE_MINUTES: Record<MonitorRange, number> = { '90m': 90, '24h': 1440, '7d': 10080, '30d': 43200 }
export const isHistoryBucketMinutes = (value: unknown): value is number => typeof value === 'number' && Number.isInteger(value) && value >= 1 && value <= 43200
export const isHistoryBarCount = (value: unknown): value is number => typeof value === 'number' && Number.isInteger(value) && value >= 1 && value <= 720

/** Resolve a browser preference to the server contract; never resample rendered bars. */
export function resolveMonitorHistory(range: MonitorRange, preferences: MonitorHistoryPreferences) {
  const rangeMinutes = MONITOR_RANGE_MINUTES[range]
  const baseMinutes = range === '7d' || range === '30d' ? 60 : 1
  const mode = preferences.historyMode
  const requestedMinutes = preferences.historyBucketMinutes
  const requestedCount = preferences.historyBarCount
  let bucketMinutes = baseMinutes === 1 ? 5 : baseMinutes
  const params: { bucket_minutes?: number; bucket_count?: number } = {}
  if (mode === 'interval' && isHistoryBucketMinutes(requestedMinutes)) {
    bucketMinutes = Math.min(rangeMinutes, Math.ceil(requestedMinutes / baseMinutes) * baseMinutes)
    params.bucket_minutes = bucketMinutes
  } else if (mode === 'count' && isHistoryBarCount(requestedCount)) {
    bucketMinutes = Math.ceil(rangeMinutes / requestedCount / baseMinutes) * baseMinutes
    params.bucket_count = requestedCount
  }
  return { params, bucketMinutes, mode, requestedMinutes, requestedCount, adjusted: mode === 'interval' && bucketMinutes !== requestedMinutes }
}
