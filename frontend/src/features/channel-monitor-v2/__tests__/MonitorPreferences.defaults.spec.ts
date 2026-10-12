import { defineComponent, h, nextTick, ref } from 'vue'
import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it } from 'vitest'
import { DEFAULT_MONITOR_PREFERENCES, provideMonitorPreferences, type MonitorPreferenceState } from '../useMonitorPreferences'
beforeEach(() => localStorage.clear())
function setup(identity = ref('user:1')) {
  let preferences!: MonitorPreferenceState
  const wrapper = mount(defineComponent({ setup() { preferences = provideMonitorPreferences(identity); return () => h('div', JSON.stringify(preferences.value)) } }))
  return { preferences, wrapper }
}
describe('administrator display defaults and personal field overrides', () => {
  it('rejects older and unversioned defaults until the account identity changes', async () => {
    const identity = ref('user:1')
    const { preferences, wrapper } = setup(identity)
    preferences.setDefaults({ ...DEFAULT_MONITOR_PREFERENCES, columns: '3' }, 8)
    preferences.setDefaults({ ...DEFAULT_MONITOR_PREFERENCES, columns: '2', autoRefresh: false }, 7)
    preferences.setDefaults({ ...DEFAULT_MONITOR_PREFERENCES, columns: '4', autoRefresh: false })
    expect(preferences.value).toMatchObject({ columns: '3', autoRefresh: true })
    preferences.reset()
    expect(preferences.value).toMatchObject({ columns: '3', autoRefresh: true })
    identity.value = 'user:2'
    await nextTick()
    preferences.setDefaults({ ...DEFAULT_MONITOR_PREFERENCES, columns: '2', autoRefresh: false }, 7)
    expect(preferences.value).toMatchObject({ columns: '2', autoRefresh: false })
    wrapper.unmount()
  })

  it('inherits administrator defaults without storing unmodified fields and preserves only customized fields', async () => {
    const { preferences, wrapper } = setup()
    preferences.setDefaults?.({ ...DEFAULT_MONITOR_PREFERENCES, columns: '3', density: 'comfortable', autoRefresh: false })
    expect(preferences.value).toMatchObject({ columns: '3', density: 'comfortable', autoRefresh: false })
    expect(localStorage.getItem('user:1:presentation')).toBeNull()
    preferences.value.columns = '4'
    expect(JSON.parse(localStorage.getItem('user:1:presentation')!)).toEqual({ __version: 2, columns: '4' })
    preferences.setDefaults?.({ ...DEFAULT_MONITOR_PREFERENCES, columns: '2', density: 'compact', motion: 'off' })
    expect(preferences.value).toMatchObject({ columns: '4', density: 'compact', motion: 'off' })
    preferences.reset?.()
    await nextTick()
    expect(preferences.value).toMatchObject({ columns: '2', motion: 'off' })
    expect(JSON.parse(localStorage.getItem('user:1:presentation')!)).toEqual({ __version: 2 })
    wrapper.unmount()
  })
  it('migrates old full snapshots by keeping changed fields and preserves explicit new overrides equal to factory values', () => {
    localStorage.setItem('user:1:presentation', JSON.stringify({ ...DEFAULT_MONITOR_PREFERENCES, density: 'comfortable' }))
    const { preferences, wrapper } = setup()
    preferences.setDefaults?.({ ...DEFAULT_MONITOR_PREFERENCES, columns: '3', metricDetails: false })
    expect(preferences.value).toMatchObject({ density: 'comfortable', columns: '3', metricDetails: false })
    preferences.value.columns = 'auto'
    wrapper.unmount()
    const restored = setup()
    restored.preferences.setDefaults?.({ ...DEFAULT_MONITOR_PREFERENCES, columns: '4', motion: 'off' })
    expect(restored.preferences.value).toMatchObject({ density: 'comfortable', columns: 'auto', motion: 'off' })
    restored.wrapper.unmount()
  })
  it('preserves selections made before defaults arrive and isolates overrides after switching identity', async () => {
    const identity = ref('user:1')
    const { preferences, wrapper } = setup(identity)
    preferences.value.historyMode = 'count'
    preferences.value.historyBarCount = 72
    preferences.setDefaults?.({ ...DEFAULT_MONITOR_PREFERENCES, historyMode: 'interval', historyBucketMinutes: 15, motion: 'off' })
    expect(preferences.value).toMatchObject({ historyMode: 'count', historyBarCount: 72, historyBucketMinutes: 15, motion: 'off' })
    identity.value = 'user:2'; await nextTick()
    preferences.setDefaults?.({ ...DEFAULT_MONITOR_PREFERENCES, historyMode: 'interval', historyBucketMinutes: 15 })
    expect(preferences.value).toMatchObject({ historyMode: 'interval', historyBarCount: 60, historyBucketMinutes: 15 })
    expect(localStorage.getItem('user:2:presentation')).toBeNull()
    wrapper.unmount()
  })
})
