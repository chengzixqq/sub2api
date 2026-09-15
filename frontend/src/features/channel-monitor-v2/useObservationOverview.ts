import { onScopeDispose, ref, shallowRef, type Ref } from 'vue'
import { getObservationOverview, type MonitorFilter, type ObservationOverview } from '@/api/channelMonitorV2'

export function useObservationOverview(filter: Ref<MonitorFilter>, admin: Ref<boolean>, userPreview: Ref<boolean>) {
  const data = shallowRef<ObservationOverview | null>(null)
  const loading = ref(false)
  const error = ref(false)
  let sequence = 0
  let controller: AbortController | null = null
  let appliedKey = ''
  let endTime = ''

  async function load(refresh = false, advance = false) {
    const id = ++sequence
    controller?.abort()
    const current = new AbortController()
    controller = current
    const frozen = { range: filter.value.range, platforms: [...filter.value.platforms], groupIds: [...filter.value.groupIds], models: [...filter.value.models] }
    const key = JSON.stringify([frozen, admin.value, userPreview.value])
    if (key !== appliedKey) data.value = null
    // A refresh represents a new snapshot boundary. Keep the boundary stable
    // only while the same request is being retried without refresh; otherwise
    // polling would repeatedly ask the server for the original (stale) end.
    if (key !== appliedKey || advance || refresh || !endTime) endTime = new Date().toISOString()
    appliedKey = key
    loading.value = true
    error.value = false
    try {
      const result = await getObservationOverview(frozen, admin.value, current.signal, { endTime, refresh, audience: userPreview.value ? 'user' : 'admin' })
      if (current.signal.aborted || id !== sequence) return
      if (result.contract_version !== 2 || result.source !== 'terminal_v1') throw new Error('Unexpected monitor contract')
      data.value = result
    } catch {
      if (current.signal.aborted || id !== sequence) return
      // A failed refresh must not leave the previous range looking current.
      // The caller can retry and the UI will render the explicit error state.
      data.value = null
      error.value = true
    } finally {
      if (id === sequence) loading.value = false
    }
  }
  onScopeDispose(() => { sequence++; controller?.abort() })
  return { data, loading, error, load }
}
