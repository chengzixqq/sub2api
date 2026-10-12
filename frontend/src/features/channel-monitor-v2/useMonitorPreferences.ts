import { computed, inject, provide, ref, watch, type InjectionKey, type Ref } from 'vue'
import type { MonitorPresentationDefaults } from '@/api/channelMonitorV2'
import { isHistoryBarCount, isHistoryBucketMinutes } from './monitorHistoryPreferences'
import './monitorPreferences.css'

export type MonitorPreferences = MonitorPresentationDefaults
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
export interface MonitorPreferenceState extends Ref<MonitorPreferences> {
  setDefaults: (value: unknown, version?: number) => void
  reset: () => void
  applyingDefaults: Ref<boolean>
}
const preferenceKey: InjectionKey<MonitorPreferenceState> = Symbol('monitor-preferences')
const keys = Object.keys(DEFAULT_MONITOR_PREFERENCES) as (keyof MonitorPreferences)[]
function overridesFrom(input: unknown, legacy = false): Partial<MonitorPreferences> {
  if (!input || typeof input !== 'object') return {}
  const raw = input as Record<string, unknown>
  const normalized = normalizeMonitorPreferences(raw)
  return Object.fromEntries(keys.filter(key => Object.prototype.hasOwnProperty.call(raw, key) && raw[key] === normalized[key] && (!legacy || normalized[key] !== DEFAULT_MONITOR_PREFERENCES[key])).map(key => [key, normalized[key]]))
}
export function provideMonitorPreferences(identity: Ref<string>, options: { isolated?: boolean; persist?: boolean } = {}) {
  const inherited = inject(preferenceKey, null)
  if (inherited && !options.isolated) return inherited
  const preferences = ref({ ...DEFAULT_MONITOR_PREFERENCES }) as MonitorPreferenceState
  let defaults = { ...DEFAULT_MONITOR_PREFERENCES }
  let defaultsVersion = -1
  let overrides: Partial<MonitorPreferences> = {}
  let previous = { ...preferences.value }
  let restoring = false
  const applyingDefaults = ref(false)
  function apply(skipUnchanged = false) {
    restoring = true
    const next = { ...defaults, ...overrides }
    // Overview polling must not reset an unfinished editor draft when nothing changed.
    if (!skipUnchanged || keys.some(key => next[key] !== preferences.value[key])) preferences.value = next
    previous = { ...preferences.value }
    restoring = false
  }
  function persist() {
    if (options.persist === false) return
    try { localStorage.setItem(`${identity.value}:presentation`, JSON.stringify({ __version: 2, ...overrides })) }
    catch { /* Browser preferences remain usable when storage is unavailable. */ }
  }
  preferences.applyingDefaults = applyingDefaults
  preferences.setDefaults = (value, version) => {
    const incomingVersion = typeof version === 'number' && Number.isSafeInteger(version) && version >= 0 ? version : -1
    // Config revision, not request completion order, determines which defaults are current.
    if (incomingVersion < defaultsVersion) return
    defaultsVersion = incomingVersion
    applyingDefaults.value = true
    defaults = normalizeMonitorPreferences(value)
    apply(true)
    applyingDefaults.value = false
  }
  preferences.reset = () => { overrides = {}; apply(); persist() }
  watch(identity, () => {
    defaults = { ...DEFAULT_MONITOR_PREFERENCES }
    defaultsVersion = -1
    overrides = {}
    if (options.persist !== false) {
      try {
        const stored = JSON.parse(localStorage.getItem(`${identity.value}:presentation`) || 'null')
        overrides = overridesFrom(stored, stored?.__version !== 2)
      } catch { /* Invalid browser data falls back to site defaults. */ }
    }
    apply()
  }, { immediate: true, flush: 'sync' })
  watch(preferences, value => {
    if (restoring) return
    const normalized = normalizeMonitorPreferences(value)
    for (const key of keys) {
      if (normalized[key] !== previous[key]) Object.assign(overrides, { [key]: normalized[key] })
    }
    previous = { ...normalized }
    persist()
  }, { deep: true, flush: 'sync' })
  provide(preferenceKey, preferences)
  return preferences
}
export function useMonitorPreferences() {
  return inject(preferenceKey, null) || Object.assign(ref({ ...DEFAULT_MONITOR_PREFERENCES }), { setDefaults: () => {}, reset: () => {}, applyingDefaults: ref(false) })
}
export function useMonitorMotion() {
  const preferences = useMonitorPreferences()
  return computed(() => preferences.value.motion === 'off' ? 'monitor-motion-off' : 'monitor-motion-system')
}
