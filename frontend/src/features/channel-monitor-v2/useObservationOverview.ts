import { onScopeDispose, ref, shallowRef, watch, type Ref } from 'vue'
import { getObservationOverview, type MonitorFilter, type ObservationOverview } from '@/api/channelMonitorV2'

export function useObservationOverview(filter: Ref<MonitorFilter>, admin: Ref<boolean>, userPreview: Ref<boolean>, identity: Ref<string> = ref(''), preview: Ref<boolean> = ref(false)) {
  const data = shallowRef<ObservationOverview | null>(null)
  const loading = ref(false)
  const error = ref(false)
  const stale = ref(false)
  const errorStatus = ref<number | null>(null)
  let sequence = 0
  let controller: AbortController | null = null
  let appliedKey = ''
  let endTime = ''

  function cancel() {
    sequence++
    controller?.abort()
    controller = null
    loading.value = false
  }

  // Cached values never cross an audience, workspace, user, or filter boundary.
  watch(() => JSON.stringify([filter.value, admin.value, userPreview.value, identity.value, preview.value]), () => {
    cancel()
    data.value = null
    appliedKey = ''
    error.value = false
    stale.value = false
    errorStatus.value = null
  }, { flush: 'sync' })

  async function load(refresh = false, advance = false) {
    const id = ++sequence
    controller?.abort()
    const current = new AbortController()
    controller = current
    const frozen = { range: filter.value.range, platforms: [...filter.value.platforms], groupIds: [...filter.value.groupIds], models: [...filter.value.models] }
    const key = JSON.stringify([frozen, admin.value, userPreview.value, identity.value, preview.value])
    if (key !== appliedKey) data.value = null
    // A refresh represents a new snapshot boundary. Keep the boundary stable
    // only while the same request is being retried without refresh; otherwise
    // polling would repeatedly ask the server for the original (stale) end.
    if (key !== appliedKey || advance || refresh || !endTime) endTime = new Date().toISOString()
    appliedKey = key
    loading.value = true
    error.value = false
    errorStatus.value = null
    try {
      const result = await getObservationOverview(frozen, admin.value, current.signal, { endTime, refresh, audience: userPreview.value ? 'user' : 'admin', preview: admin.value && preview.value })
      if (current.signal.aborted || id !== sequence) return
      if (result.contract_version !== 2 || !['compact', 'legacy', 'mixed', 'terminal_v1'].includes(result.source)) throw new Error('Unexpected monitor contract')
      data.value = result
      stale.value = false
    } catch (cause) {
      if (current.signal.aborted || id !== sequence) return
      const status = (cause as { response?: { status?: number } })?.response?.status
      errorStatus.value = status ?? null
      // Authorization failures revoke the snapshot even without a local role change.
      if (status === 401 || status === 403) data.value = null
      error.value = true
      stale.value = true
    } finally {
      if (id === sequence) loading.value = false
    }
  }
  onScopeDispose(cancel)
  return { data, loading, error, stale, errorStatus, load, cancel }
}
