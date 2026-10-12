import { defineComponent, h, ref } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { MonitorPresentationDefaults, MonitorPresentationSnapshot, ObservationOverview } from '@/api/channelMonitorV2'
import { DEFAULT_MONITOR_PREFERENCES, provideMonitorPreferences, type MonitorPreferenceState } from '../useMonitorPreferences'
import { useMonitorPresentationDefaults } from '../useMonitorPresentationDefaults'

const api = vi.hoisted(() => ({ getMonitorPresentationDefaults: vi.fn() }))
vi.mock('@/api/channelMonitorV2', () => api)
beforeEach(() => { localStorage.clear(); api.getMonitorPresentationDefaults.mockReset() })
const snapshot = (version: number, preferences: Partial<MonitorPresentationDefaults>): MonitorPresentationSnapshot => ({
  presentation_defaults_version: version,
  presentation_defaults: { ...DEFAULT_MONITOR_PREFERENCES, ...preferences },
})

function setup(enabled = ref(true)) {
  const identity = ref('user:1')
  const admin = ref(false)
  const overview = ref<ObservationOverview | null>(null)
  let preferences!: MonitorPreferenceState
  const wrapper = mount(defineComponent({ setup() {
    preferences = provideMonitorPreferences(identity)
    useMonitorPresentationDefaults(preferences, identity, admin, overview, enabled)
    return () => h('div')
  } }))
  return { preferences, identity, overview, wrapper }
}

describe('display defaults loading', () => {
  it('loads administrator defaults independently of an unavailable overview and preserves personal fields', async () => {
    localStorage.setItem('user:1:presentation', JSON.stringify({ __version: 2, columns: '4' }))
    api.getMonitorPresentationDefaults.mockResolvedValue(snapshot(8, { columns: '2', defaultView: 'matrix', defaultRange: '7d' }))
    const { preferences, wrapper } = setup()
    await flushPromises()
    expect(api.getMonitorPresentationDefaults).toHaveBeenCalledWith(false, expect.any(AbortSignal))
    expect(preferences.value).toMatchObject({ columns: '4', defaultView: 'matrix', defaultRange: '7d' })
    expect(JSON.parse(localStorage.getItem('user:1:presentation')!)).toEqual({ __version: 2, columns: '4' })
    wrapper.unmount()
  })

  it('rejects a late metadata response after an account change', async () => {
    let resolveOld!: (value: MonitorPresentationSnapshot) => void
    api.getMonitorPresentationDefaults.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve }))
    api.getMonitorPresentationDefaults.mockResolvedValue(snapshot(7, { columns: '3' }))
    const { preferences, identity, wrapper } = setup()
    const signal = api.getMonitorPresentationDefaults.mock.calls[0][1] as AbortSignal
    identity.value = 'user:2'
    await flushPromises()
    resolveOld(snapshot(9, { columns: '2' }))
    await flushPromises()
    expect(signal.aborted).toBe(true)
    expect(preferences.value.columns).toBe('3')
    wrapper.unmount()
  })

  it('uses overview defaults and rejects older outstanding metadata', async () => {
    let resolveOld!: (value: MonitorPresentationSnapshot) => void
    api.getMonitorPresentationDefaults.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve }))
    const { preferences, overview, wrapper } = setup()
    overview.value = snapshot(8, { columns: '3' }) as ObservationOverview
    resolveOld(snapshot(7, { columns: '2', autoRefresh: false }))
    await flushPromises()
    expect(preferences.value).toMatchObject({ columns: '3', autoRefresh: true })
    wrapper.unmount()
  })

  it.each([true, false])('keeps newer metadata defaults regardless of whether it arrives first (%s)', async metadataFirst => {
    let resolveMetadata!: (value: MonitorPresentationSnapshot) => void
    api.getMonitorPresentationDefaults.mockImplementationOnce(() => new Promise(resolve => { resolveMetadata = resolve }))
    const { preferences, overview, wrapper } = setup()
    const oldOverview = snapshot(7, { columns: '2', autoRefresh: false }) as ObservationOverview
    const newMetadata = snapshot(8, { columns: '3', autoRefresh: true })
    if (metadataFirst) {
      resolveMetadata(newMetadata)
      await flushPromises()
      overview.value = oldOverview
    } else {
      overview.value = oldOverview
      resolveMetadata(newMetadata)
    }
    await flushPromises()
    expect(preferences.value).toMatchObject({ columns: '3', autoRefresh: true })
    wrapper.unmount()
  })

  it('does not request site defaults in local preview mode', async () => {
    const { wrapper } = setup(ref(false))
    await flushPromises()
    expect(api.getMonitorPresentationDefaults).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
