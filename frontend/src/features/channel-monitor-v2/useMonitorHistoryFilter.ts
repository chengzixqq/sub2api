import { watch, type Ref } from 'vue'
import type { MonitorFilter } from '@/api/channelMonitorV2'
import { resolveMonitorHistory, type MonitorHistoryPreferences } from './monitorHistoryPreferences'

/** Keep identity resets, range switches, details, and request cache keys on one query. */
export function useMonitorHistoryFilter(filter: Ref<MonitorFilter>, preferences: Ref<MonitorHistoryPreferences>) {
  watch(() => [filter.value.range, filter.value.bucket_minutes, filter.value.bucket_count, preferences.value.historyMode, preferences.value.historyBucketMinutes, preferences.value.historyBarCount], () => {
    const params = resolveMonitorHistory(filter.value.range, preferences.value).params
    if (filter.value.bucket_minutes === params.bucket_minutes && filter.value.bucket_count === params.bucket_count) return
    const next = { ...filter.value }
    delete next.bucket_minutes
    delete next.bucket_count
    filter.value = { ...next, ...params }
  }, { immediate: true, flush: 'sync' })
}
