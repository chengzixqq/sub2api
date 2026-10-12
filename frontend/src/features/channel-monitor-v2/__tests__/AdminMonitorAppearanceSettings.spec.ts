import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h, ref } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AdminMonitorAppearanceSettings from '../AdminMonitorAppearanceSettings.vue'
import { DEFAULT_MONITOR_PREFERENCES, provideMonitorPreferences, type MonitorPreferenceState } from '../useMonitorPreferences'
import { useMonitorPresentationDefaults } from '../useMonitorPresentationDefaults'
import type { ObservationOverview } from '@/api/channelMonitorV2'
const api = vi.hoisted(() => ({ getObservationConfig: vi.fn(), updateObservationConfig: vi.fn(), getMonitorPresentationDefaults: vi.fn() }))
vi.mock('@/api/channelMonitorV2', () => api)
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, locale: { value: 'en' } }) }))
const config = { version: 7, probe_enabled: true, probe_global_daily_limit: 42, presentation_defaults: { ...DEFAULT_MONITOR_PREFERENCES, columns: '3' } }
beforeEach(() => { localStorage.clear(); api.getObservationConfig.mockReset().mockResolvedValue(structuredClone(config)); api.updateObservationConfig.mockReset().mockImplementation(async value => ({ ...value, version: 8 })) })
const host = () => mount(defineComponent({ setup() { provideMonitorPreferences(ref('admin:1')); return () => h(AdminMonitorAppearanceSettings) } }))
describe('site display defaults editor', () => {
  it('keeps newly saved defaults when an older in-flight overview arrives', async () => {
    const overview = ref<ObservationOverview | null>(null)
    let preferences!: MonitorPreferenceState
    api.getMonitorPresentationDefaults.mockRejectedValue(new Error('offline'))
    const wrapper = mount(defineComponent({ setup() {
      const identity = ref('admin:1')
      preferences = provideMonitorPreferences(identity)
      useMonitorPresentationDefaults(preferences, identity, ref(true), overview, ref(true))
      return () => h(AdminMonitorAppearanceSettings)
    } }))
    await flushPromises()
    await wrapper.get('[data-testid="pref-metric-details"]').trigger('click')
    await wrapper.get('[data-testid="save-site-appearance"]').trigger('click')
    await flushPromises()
    expect(preferences.value.metricDetails).toBe(false)
    overview.value = {
      presentation_defaults_version: 7,
      presentation_defaults: { ...DEFAULT_MONITOR_PREFERENCES, columns: '2', autoRefresh: false },
    } as ObservationOverview
    await flushPromises()
    expect(preferences.value).toMatchObject({ columns: '3', metricDetails: false, autoRefresh: true })
    wrapper.unmount()
  })

  it('saves all users defaults with the loaded version and preserves collection policy', async () => {
    const wrapper = host(); await flushPromises()
    await wrapper.get('[data-testid="pref-metric-details"]').trigger('click')
    expect(localStorage.getItem('admin:1:presentation')).toBeNull()
    expect(api.updateObservationConfig).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="save-site-appearance"]').trigger('click'); await flushPromises()
    expect(api.updateObservationConfig).toHaveBeenCalledWith({ ...config, presentation_defaults: { ...config.presentation_defaults, metricDetails: false } })
    expect(wrapper.text()).toContain('Site defaults saved')
    wrapper.unmount()
  })
  it('retains the draft on conflict and requires an explicit reload before another save', async () => {
    api.updateObservationConfig.mockRejectedValue({ response: { status: 409 } })
    const wrapper = host(); await flushPromises()
    await wrapper.get('[data-testid="pref-metric-details"]').trigger('click')
    await wrapper.get('[data-testid="save-site-appearance"]').trigger('click'); await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('Your draft is retained')
    expect(wrapper.get('[data-testid="pref-metric-details"]').attributes('aria-checked')).toBe('false')
    expect(wrapper.get('[data-testid="save-site-appearance"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })
})
