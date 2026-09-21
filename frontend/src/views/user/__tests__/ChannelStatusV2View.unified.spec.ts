import { defineComponent, h, reactive } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ChannelStatusV2View from '../ChannelStatusV2View.vue'

const api = vi.hoisted(() => ({ getObservationOverview: vi.fn(), getSnapshot: vi.fn(), getMatrix: vi.fn(), getDimensions: vi.fn() }))
const stores = vi.hoisted(() => ({ auth: { user: { id: 1, role: 'admin' }, workspace: { id: 1 } } }))
const route = vi.hoisted(() => ({ query: {} as Record<string, string> }))
vi.mock('@/api/channelMonitorV2', () => api)
vi.mock('@/stores/auth', () => ({ useAuthStore: () => reactive(stores.auth) }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn() }) }))
vi.mock('vue-router', () => ({ useRoute: () => route, useRouter: () => ({ replace: vi.fn() }) }))
vi.mock('vue-i18n', async (importOriginal) => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key, te: () => false, locale: { value: 'en' } }) }))
const cards = defineComponent({
  name: 'ObservationCards', props: ['overview', 'layout'], emits: ['bucket'],
  setup(props, { emit }) {
    return () => h('button', { 'data-testid': 'open-bucket', onClick: () => emit('bucket', props.overview.items[0], { start: '2026-09-18T00:00:00Z', bucket: null }) }, 'group')
  },
})
const snapshot = () => ({
  contract_version: 2, source: 'compact', mode: 'live',
  coverage: { state: 'complete', collector_state: 'healthy', data_through: '2026-09-18T00:00:00Z', bucket_seconds: 300, gap_reasons: [] },
  dimensions: { platforms: [], groups: [], models: [] },
  items: [{ group_id: 1, group_name: 'Group 1', platform: 'openai', models: [], buckets: [], metrics: {}, health: {} }],
})
const mountView = () => mount(ChannelStatusV2View, { global: { stubs: {
  AppLayout: { template: '<main><slot /></main>' }, Icon: true, LoadingSpinner: true,
  FilterMultiSelect: true, ObservationCards: cards,
  ObservationDetailDrawer: { props: ['show'], template: '<div v-if="show" data-testid="bucket-detail" />' },
} } })

describe('unified channel monitor page', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.useFakeTimers()
    api.getObservationOverview.mockResolvedValue(snapshot())
    stores.auth.user.role = 'admin'
    route.query = {}
    localStorage.clear()
  })
  afterEach(() => vi.useRealTimers())

  it.each(['admin', 'vendor', 'user'])('uses only overview for %s with a 24h default', async (role) => {
    stores.auth.user.role = role
    const wrapper = mountView()
    await flushPromises()
    expect(api.getObservationOverview).toHaveBeenCalledWith(
      expect.objectContaining({ range: '24h' }), role === 'admin', expect.anything(), expect.anything(),
    )
    expect(api.getSnapshot).not.toHaveBeenCalled()
    expect(api.getMatrix).not.toHaveBeenCalled()
    expect(api.getDimensions).not.toHaveBeenCalled()
    expect(wrapper.text()).not.toContain('switchToLegacy')
    wrapper.unmount()
  })

  it('opens bucket detail and polls every 60 seconds', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="open-bucket"]').trigger('click')
    expect(wrapper.find('[data-testid="bucket-detail"]').exists()).toBe(true)
    await vi.advanceTimersByTimeAsync(60_000)
    expect(api.getObservationOverview).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })
  it('defaults to the group list without inheriting old card preferences', async () => {
    localStorage.setItem('sub2api:observation:v2:1:admin:1:layout', 'cards')
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.findComponent(cards).props('layout')).toBe('list')
    wrapper.unmount()
  })
  it('revokes an open detail when access is lost during refresh', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="open-bucket"]').trigger('click')
    api.getObservationOverview.mockRejectedValueOnce({ response: { status: 403 } })
    await vi.advanceTimersByTimeAsync(60_000)
    await flushPromises()
    expect(wrapper.find('[data-testid="bucket-detail"]').exists()).toBe(false)
    wrapper.unmount()
  })
  it('keeps the published compact source when collection is disabled', async () => {
    const disabled = snapshot()
    disabled.coverage.collector_state = 'disabled'
    disabled.coverage.state = 'unavailable'
    api.getObservationOverview.mockResolvedValue(disabled)
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.text()).toContain('channelMonitorV2.unified.collector.disabled')
    expect(wrapper.text()).not.toContain('channelMonitorV2.unified.legacySource')
    expect(api.getSnapshot).not.toHaveBeenCalled()
    wrapper.unmount()
  })
  it.each(['admin', 'vendor', 'user'])('shows backlog state but protects event counts for %s', async role => {
    stores.auth.user.role = role
    const queued = snapshot()
    api.getObservationOverview.mockResolvedValue({ ...queued, coverage: { ...queued.coverage, collector_state: 'backlogged', pending_events: 17 } })
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.text()).toContain('channelMonitorV2.unified.collector.backlogged')
    expect(wrapper.text().includes('channelMonitorV2.unified.pendingEvents')).toBe(role === 'admin')
    wrapper.unmount()
  })
})
