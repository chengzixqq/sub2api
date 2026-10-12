import { onMounted, onScopeDispose, watch, type Ref } from 'vue'
import { getMonitorPresentationDefaults, type ObservationOverview } from '@/api/channelMonitorV2'
import type { MonitorPreferenceState } from './useMonitorPreferences'

export function useMonitorPresentationDefaults(preferences: MonitorPreferenceState, identity: Ref<string>, admin: Ref<boolean>, overview: Ref<ObservationOverview | null>, enabled: Ref<boolean>) {
  let controller: AbortController | null = null
  let sequence = 0
  async function load() {
    const request = ++sequence
    controller?.abort()
    if (!enabled.value) return
    const current = new AbortController()
    controller = current
    try {
      const snapshot = await getMonitorPresentationDefaults(admin.value, current.signal)
      if (request === sequence && !current.signal.aborted) preferences.setDefaults(snapshot.presentation_defaults, snapshot.presentation_defaults_version)
    } catch { /* Keep current defaults and personal preferences usable when offline. */ }
  }
  watch(overview, value => {
    if (enabled.value && value?.presentation_defaults) {
      // A slow overview may hold an older policy than metadata or a completed admin save.
      preferences.setDefaults(value.presentation_defaults, value.presentation_defaults_version)
    }
  }, { flush: 'sync' })
  watch([identity, admin, enabled], load, { flush: 'sync' })
  onMounted(load)
  onScopeDispose(() => { sequence++; controller?.abort() })
}
