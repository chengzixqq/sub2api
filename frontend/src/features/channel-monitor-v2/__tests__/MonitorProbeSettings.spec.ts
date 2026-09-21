import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import MonitorProbeSettings from '../MonitorProbeSettings.vue'
const api = vi.hoisted(() => ({ getProbeTargets: vi.fn(), getProbeBudget: vi.fn(), createProbeTarget: vi.fn(), updateProbeTarget: vi.fn(), runProbeTarget: vi.fn(), getAll: vi.fn(), showError: vi.fn(), showSuccess: vi.fn() }))
vi.mock('@/api/channelMonitorV2', () => api)
vi.mock('@/api/admin', () => ({ adminAPI: { groups: { getAll: api.getAll } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => api }))
vi.mock('vue-i18n', async (importOriginal) => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))
const target = { id: 9, group_id: 7, model: 'gpt-test', protocol: 'openai_responses', enabled: false, version: 1 }
const stubs = { Icon: true, BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' }, ConfirmDialog: { props: ['show'], emits: ['confirm'], template: '<button v-if="show" data-testid="confirm-probe" @click="$emit(\'confirm\')">Confirm</button>' } }
describe('probe target controls', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.getProbeTargets.mockResolvedValue({ items: [target], total: 1 })
    api.getProbeBudget.mockResolvedValue({ utc_day: '2026-09-18', global_limit: 1000, global_used: 0, targets: [{ target_id: 9, limit: 96, used: 0 }] })
    api.getAll.mockResolvedValue([{ id: 7, name: 'Group 7' }])
  })
  it('creates disabled targets so a new target cannot start billable probes implicitly', async () => {
    api.createProbeTarget.mockResolvedValue(target)
    const wrapper = mount(MonitorProbeSettings, { global: { stubs } })
    await flushPromises()
    await wrapper.get('[data-testid="add-target"]').trigger('click')
    await wrapper.get('[data-testid="target-group"]').setValue('7')
    await wrapper.get('[data-testid="target-model"]').setValue('gpt-test')
    await wrapper.get('[data-testid="target-form"]').trigger('submit')
    expect(api.createProbeTarget).toHaveBeenCalledWith(expect.objectContaining({ group_id: 7, model: 'gpt-test', enabled: false }))
  })
  it('requires confirmation and reuses the idempotency key after an ambiguous network failure', async () => {
    api.runProbeTarget.mockRejectedValueOnce(new Error('network')).mockResolvedValueOnce({ status: 'healthy' })
    const wrapper = mount(MonitorProbeSettings, { global: { stubs } })
    await flushPromises()
    await wrapper.get('[data-testid="probe-9"]').trigger('click')
    expect(api.runProbeTarget).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="confirm-probe"]').trigger('click')
    await flushPromises()
    const key = api.runProbeTarget.mock.calls[0]![1]
    expect(key).toMatch(/^[a-zA-Z0-9-]{16,128}$/)
    await wrapper.get('[data-testid="probe-9"]').trigger('click')
    await wrapper.get('[data-testid="confirm-probe"]').trigger('click')
    await flushPromises()
    expect(api.runProbeTarget.mock.calls[1]![1]).toBe(key)
  })
})
