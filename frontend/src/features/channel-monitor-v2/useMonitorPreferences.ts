import { computed, inject, provide, ref, watch, type InjectionKey, type Ref } from 'vue'
import type { MonitorRange } from '@/api/channelMonitorV2'
import { isHistoryBarCount, isHistoryBucketMinutes, type MonitorHistoryPreferences } from './monitorHistoryPreferences'
import './monitorPreferences.css'

export interface MonitorPreferences extends MonitorHistoryPreferences {
  density: 'compact' | 'comfortable'
  columns: 'auto' | '2' | '3' | '4'
  motion: 'system' | 'off'
  metricDetails: boolean
  expandModels: boolean
  autoRefresh: boolean
  defaultView: 'cards' | 'matrix' | 'trend'
  defaultRange: MonitorRange
}
export const DEFAULT_MONITOR_PREFERENCES: MonitorPreferences = {
  density: 'compact', columns: 'auto', motion: 'system', metricDetails: true,
  expandModels: false, autoRefresh: true, defaultView: 'cards', defaultRange: '24h',
  historyMode: 'auto', historyBucketMinutes: 5, historyBarCount: 60,
}
export function normalizeMonitorPreferences(input: unknown): MonitorPreferences {
  const result = { ...DEFAULT_MONITOR_PREFERENCES }
  if (!input || typeof input !== 'object') return result
  const raw = input as Record<string, unknown>
  const enums = { density: ['compact', 'comfortable'], columns: ['auto', '2', '3', '4'], motion: ['system', 'off'], defaultView: ['cards', 'matrix', 'trend'], defaultRange: ['90m', '24h', '7d', '30d'], historyMode: ['auto', 'interval', 'count'] }
  for (const [key, values] of Object.entries(enums)) {
    if (typeof raw[key] === 'string' && values.includes(raw[key] as string)) Object.assign(result, { [key]: raw[key] })
  }
  for (const key of ['metricDetails', 'expandModels', 'autoRefresh'] as const) if (typeof raw[key] === 'boolean') result[key] = raw[key]
  if (isHistoryBucketMinutes(raw.historyBucketMinutes)) result.historyBucketMinutes = raw.historyBucketMinutes
  if (isHistoryBarCount(raw.historyBarCount)) result.historyBarCount = raw.historyBarCount
  return result
}
const preferenceKey: InjectionKey<Ref<MonitorPreferences>> = Symbol('monitor-preferences')
export function provideMonitorPreferences(identity: Ref<string>) {
  const inherited = inject(preferenceKey, null)
  if (inherited) return inherited
  const preferences = ref({ ...DEFAULT_MONITOR_PREFERENCES })
  let restoring = false
  watch(identity, value => {
    restoring = true
    try { preferences.value = normalizeMonitorPreferences(JSON.parse(localStorage.getItem(`${value}:presentation`) || 'null')) }
    catch { preferences.value = { ...DEFAULT_MONITOR_PREFERENCES } }
    finally { restoring = false }
  }, { immediate: true, flush: 'sync' })
  watch(preferences, value => {
    if (restoring) return
    try { localStorage.setItem(`${identity.value}:presentation`, JSON.stringify(normalizeMonitorPreferences(value))) }
    catch { /* Browser preferences remain usable when storage is unavailable. */ }
  }, { deep: true, flush: 'sync' })
  provide(preferenceKey, preferences)
  return preferences
}
export function useMonitorPreferences() {
  return inject(preferenceKey, ref({ ...DEFAULT_MONITOR_PREFERENCES }))
}
export function useMonitorMotion() {
  const preferences = useMonitorPreferences()
  return computed(() => preferences.value.motion === 'off' ? 'monitor-motion-off' : 'monitor-motion-system')
}
