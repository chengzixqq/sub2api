import { defineComponent, h, nextTick, ref } from 'vue'
import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { DEFAULT_MONITOR_PREFERENCES, normalizeMonitorPreferences, provideMonitorPreferences, useMonitorPreferences } from '../useMonitorPreferences'
import { useMonitorPolling } from '../useMonitorPolling'

beforeEach(() => { localStorage.clear(); vi.useFakeTimers() })
afterEach(() => { vi.useRealTimers(); localStorage.clear(); vi.restoreAllMocks() })
describe('monitor preferences and polling', () => {
  it('isolates changes by identity, restores them and rejects malformed local values', async () => {
    const identity = ref('user:1')
    const child = defineComponent({ setup() { const preferences = useMonitorPreferences(); return () => h('button', { onClick: () => { preferences.value.density = 'comfortable' } }, preferences.value.density) } })
    const host = mount(defineComponent({ setup() { provideMonitorPreferences(identity); return () => h(child) } }))
    await host.get('button').trigger('click')
    expect(JSON.parse(localStorage.getItem('user:1:presentation')!).density).toBe('comfortable')
    identity.value = 'user:2'; await nextTick()
    expect(host.text()).toBe('compact')
    expect(localStorage.getItem('user:2:presentation')).toBeNull()
    identity.value = 'user:1'; await nextTick()
    expect(host.text()).toBe('comfortable')
    expect(normalizeMonitorPreferences({ columns: '500', motion: 'force', autoRefresh: 'true', defaultRange: 'forever' })).toEqual(DEFAULT_MONITOR_PREFERENCES)
    host.unmount()
  })
  it('uses the returned site interval, responds to changes and stops disabled or unmounted timers', async () => {
    const seconds = ref<number | undefined>(30)
    const enabled = ref(true)
    const loading = ref(false)
    const refresh = vi.fn()
    const wrapper = mount(defineComponent({ setup() { useMonitorPolling(refresh, seconds, enabled, loading); return () => h('div') } }))
    await vi.advanceTimersByTimeAsync(29_999); expect(refresh).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(1); expect(refresh).toHaveBeenCalledTimes(1)
    seconds.value = 120; await nextTick()
    await vi.advanceTimersByTimeAsync(60_000); expect(refresh).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(60_000); expect(refresh).toHaveBeenCalledTimes(2)
    loading.value = true
    await vi.advanceTimersByTimeAsync(120_000); expect(refresh).toHaveBeenCalledTimes(2)
    loading.value = false; enabled.value = false; await nextTick()
    await vi.advanceTimersByTimeAsync(300_000); expect(refresh).toHaveBeenCalledTimes(2)
    document.dispatchEvent(new Event('visibilitychange')); expect(refresh).toHaveBeenCalledTimes(2)
    enabled.value = true; seconds.value = 0; await nextTick()
    await vi.advanceTimersByTimeAsync(60_000); expect(refresh).toHaveBeenCalledTimes(3)
    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(300_000); expect(refresh).toHaveBeenCalledTimes(3)
  })
})
