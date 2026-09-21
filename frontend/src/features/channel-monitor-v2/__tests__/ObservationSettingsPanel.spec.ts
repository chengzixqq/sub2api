import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ObservationSettingsPanel from '../ObservationSettingsPanel.vue'
const api = vi.hoisted(() => ({ getObservationConfig: vi.fn(), updateObservationConfig: vi.fn(), getAll: vi.fn(), showSuccess: vi.fn(), showError: vi.fn() }))
vi.mock('@/api/channelMonitorV2', () => api)
vi.mock('@/api/admin', () => ({ adminAPI: { groups: { getAll: api.getAll } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => api }))
vi.mock('vue-i18n', async (importOriginal) => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))
const policy = { version: 3, enabled: true, mode: 'shadow', live_group_ids: [], detail_retention_hours: 24, refresh_interval_seconds: 60, minimum_sample: 50, healthy_reliability: .99, warning_reliability: .95, warning_ttft_ms: 3000, critical_ttft_ms: 10000, overrides: [] }
describe('observation policy editor', () => {
  beforeEach(() => {
    api.getObservationConfig.mockResolvedValue(policy)
    api.updateObservationConfig.mockImplementation(async value => value)
    api.getAll.mockResolvedValue([{ id: 7, name: 'Group 7' }])
  })
  it('saves the actual observation policy including publication scope and version', async () => {
    const wrapper = mount(ObservationSettingsPanel, { global: { stubs: { Icon: true } } })
    await flushPromises()
    await wrapper.get('[data-testid="publication"]').setValue('live')
    await wrapper.get('[data-testid="live-group-7"]').setValue(true)
    await wrapper.get('form').trigger('submit')
    expect(api.updateObservationConfig).toHaveBeenCalledWith(expect.objectContaining({ version: 3, mode: 'live', live_group_ids: [7], detail_retention_hours: 24, refresh_interval_seconds: 60 }))
  })
  it('keeps collection, paid probes and quota monitoring independent with safe defaults', async () => {
    const wrapper = mount(ObservationSettingsPanel, { global: { stubs: { Icon: true } } })
    await flushPromises()
    expect((wrapper.get('[data-testid="probe-enabled"]').element as HTMLInputElement).checked).toBe(false)
    expect((wrapper.get('[data-testid="quota-enabled"]').element as HTMLInputElement).checked).toBe(true)
    await wrapper.get('[data-testid="collection-enabled"]').setValue(false)
    await wrapper.get('[data-testid="probe-enabled"]').setValue(true)
    await wrapper.get('form').trigger('submit')
    expect(api.updateObservationConfig).toHaveBeenCalledWith(expect.objectContaining({ enabled: false, probe_enabled: true, quota_enabled: true }))
  })
})
