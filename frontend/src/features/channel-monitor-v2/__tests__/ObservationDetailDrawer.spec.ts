import { mount, flushPromises } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import ObservationDetailDrawer from '../ObservationDetailDrawer.vue'
import type { ObservationChannel } from '@/api/channelMonitorV2'
const api = vi.hoisted(() => ({ getMonitorAccountDetail: vi.fn() }))
vi.mock('@/api/channelMonitorV2', () => api)
vi.mock('vue-i18n', async (importOriginal) => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key, te: () => false, locale: { value: 'en' } }) }))
const item = { group_id: 1, group_name: 'Visible group', models: [], metrics: { reliability_rate: 1, ttft: { p50_ms: 300, p95_ms: 500 }, cache_rate: null }, health: { reliability: 'healthy', latency: 'healthy' } } as unknown as ObservationChannel
const props = { show: true, item, selection: null, filter: { range: '24h' as const, platforms: [], groupIds: [], models: [] }, identity: 'user:1' }
describe('monitor detail privacy', () => {
  it('does not load accounts for regular users', async () => {
    const wrapper = mount(ObservationDetailDrawer, { props, global: { stubs: { Teleport: true, Icon: true } } })
    await flushPromises()
    expect(api.getMonitorAccountDetail).not.toHaveBeenCalled()
    expect(wrapper.text()).not.toContain('unified.accounts')
    wrapper.unmount()
  })
  it('clears owner-only detail immediately when the audience changes', async () => {
    api.getMonitorAccountDetail.mockResolvedValue({ items: [{ account_id: 99, metrics: item.metrics }], samples: [] })
    const wrapper = mount(ObservationDetailDrawer, { props: { ...props, admin: true }, global: { stubs: { Teleport: true, Icon: true } } })
    await flushPromises()
    expect(wrapper.text()).toContain('#99')
    await wrapper.setProps({ admin: false, identity: 'vendor:1' })
    expect(wrapper.text()).not.toContain('#99')
    wrapper.unmount()
  })
  it('marks current status stale when the overview refresh fails', () => {
    const wrapper = mount(ObservationDetailDrawer, { props: { ...props, stale: true, item: { ...item, current_status: { state: 'healthy', source: 'traffic', reason: 'traffic_healthy' } } }, global: { stubs: { Teleport: true, Icon: true } } })
    expect(wrapper.text()).toContain('channelMonitorV2.unified.states.stale')
    wrapper.unmount()
  })
  it('shows account attempt outcomes rather than fabricated request metrics', async () => {
    api.getMonitorAccountDetail.mockResolvedValue({ items: [{ account_id: 7, metrics: { attempt_count: 23, success_requests: 19, channel_errors: 4 } }], samples: [] })
    const wrapper = mount(ObservationDetailDrawer, { props: { ...props, admin: true }, global: { stubs: { Teleport: true, Icon: true } } })
    await flushPromises()
    expect(wrapper.get('tbody').text()).toContain('23')
    expect(wrapper.get('tbody').text()).toContain('19')
    expect(wrapper.get('tbody').text()).toContain('4')
    expect(wrapper.get('thead').text()).toContain('channelMonitorV2.observation.attempts')
    wrapper.unmount()
  })
  it('narrows account detail to the selected expanded model', async () => {
    api.getMonitorAccountDetail.mockResolvedValue({ items: [], samples: [] })
    const model = { model: 'selected-model', metrics: item.metrics, health: item.health, buckets: [] }
    const wrapper = mount(ObservationDetailDrawer, { props: { ...props, admin: true, model: model.model, item: { ...item, models: [model] } }, global: { stubs: { Teleport: true, Icon: true } } })
    await flushPromises()
    expect(api.getMonitorAccountDetail).toHaveBeenLastCalledWith(1, expect.objectContaining({ models: ['selected-model'] }), expect.anything())
    expect(wrapper.text()).toContain('selected-model')
    wrapper.unmount()
  })
})
