import { mount, flushPromises } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import ObservationDetailDrawer from '../ObservationDetailDrawer.vue'
import MonitorTrendPanel from '../MonitorTrendPanel.vue'
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
    const wrapper = mount(ObservationDetailDrawer, { props: { ...props, admin: true, owner: true }, global: { stubs: { Teleport: true, Icon: true } } })
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
    const wrapper = mount(ObservationDetailDrawer, { props: { ...props, admin: true, owner: true }, global: { stubs: { Teleport: true, Icon: true } } })
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
    const wrapper = mount(ObservationDetailDrawer, { props: { ...props, admin: true, owner: true, model: model.model, item: { ...item, models: [model] } }, global: { stubs: { Teleport: true, Icon: true } } })
    await flushPromises()
    expect(api.getMonitorAccountDetail).toHaveBeenLastCalledWith(1, expect.objectContaining({ models: ['selected-model'] }), expect.anything())
    expect(wrapper.text()).toContain('selected-model')
    wrapper.unmount()
  })
  it('uses the selected model history in the actual trend panel', async () => {
    const modelMetrics = { ...item.metrics, sample_state: 'sufficient' as const, reliability_rate: 0.8 }
    const buckets = [{ bucket_start: '2026-09-20T06:00:00Z', metrics: modelMetrics, health: item.health }]
    const model = { model: 'selected-model', metrics: modelMetrics, health: item.health, buckets }
    const wrapper = mount(ObservationDetailDrawer, { props: { ...props, admin: true, model: model.model, item: { ...item, models: [model], buckets: [] } }, global: { stubs: { Teleport: true, Icon: true, MonitorTrendPanel: true } } })
    await wrapper.get('[data-testid="detail-tab-trend"]').trigger('click')
    const selected = wrapper.getComponent(MonitorTrendPanel).props('items')[0]
    expect(selected.metrics).toEqual(modelMetrics)
    expect(selected.buckets).toEqual(buckets)
    expect(selected.group_name).toContain(model.model)
    wrapper.unmount()
  })
  it('distinguishes no samples, missing data and observed zero channel errors', async () => {
    const wrapper = mount(ObservationDetailDrawer, { props: { ...props, admin: true, item: { ...item, metrics: { ...item.metrics, sample_state: 'no_samples', channel_errors: 0 } } }, global: { stubs: { Teleport: true, Icon: true } } })
    await wrapper.get('[data-testid="detail-tab-errors"]').trigger('click')
    expect(wrapper.get('[data-testid="detail-error-empty"]').text()).toContain('states.no_samples')
    await wrapper.setProps({ selection: { start: '2026-09-20T06:00:00Z', bucket: null } })
    expect(wrapper.get('[data-testid="detail-error-empty"]').text()).toContain('states.missing')
    await wrapper.setProps({ selection: null, item: { ...item, metrics: { ...item.metrics, sample_state: 'sufficient', channel_errors: 0 } } })
    expect(wrapper.get('[data-testid="detail-error-empty"]').text()).toContain('No channel errors in observed traffic')
    await wrapper.setProps({ item: { ...item, metrics: { ...item.metrics, sample_state: 'sufficient', channel_errors: undefined } } })
    expect(wrapper.get('[data-testid="detail-error-empty"]').text()).toContain('does not imply a zero error rate')
    wrapper.unmount()
  })
  it('shows concrete gap reasons and the fallback source in data quality', async () => {
    const coverage = { state: 'partial', gap_reasons: ['collector_stale', 'queue_overflow'], aggregation_lag_seconds: 120 } as never
    const wrapper = mount(ObservationDetailDrawer, { props: { ...props, admin: true, coverage, source: 'terminal_v1' }, global: { stubs: { Teleport: true, Icon: true } } })
    await wrapper.get('[data-testid="detail-tab-quality"]').trigger('click')
    expect(wrapper.text()).toContain('collector_stale')
    expect(wrapper.text()).toContain('queue_overflow')
    expect(wrapper.text()).toContain('terminal_v1')
    wrapper.unmount()
  })

})
