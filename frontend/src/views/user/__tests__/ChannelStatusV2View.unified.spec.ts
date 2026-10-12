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
    stores.auth.user.id = 1
    stores.auth.user.role = 'admin'
    stores.auth.workspace.id = 1
    route.query = {}
    localStorage.clear()
  })
  afterEach(() => vi.useRealTimers())

  it.each(['user', 'vendor', 'admin'])('keeps all four range buttons visible and requests the selected window for %s', async role => {
    stores.auth.user.role = role
    const wrapper = mountView()
    await flushPromises()
    const buttons = wrapper.findAll('[data-testid="monitor-range-selector"] button')
    expect(buttons).toHaveLength(4)
    for (const [index, range] of ['90m', '24h', '7d', '30d'].entries()) {
      await buttons[index]!.trigger('click')
      await flushPromises()
      expect(buttons[index]!.attributes('aria-pressed')).toBe('true')
      expect(api.getObservationOverview.mock.lastCall![0].range).toBe(range)
      expect(api.getObservationOverview.mock.lastCall![1]).toBe(role === 'admin')
    }
    wrapper.unmount()
  })

  it('restores history preferences independently across user workspaces and page reloads', async () => {
    stores.auth.user.role = 'user'
    const auth = reactive(stores.auth)
    const key = (user: number, workspace: number) => `sub2api:monitor-unified:v1:${user}:user:${workspace}:presentation`
    localStorage.setItem(key(1, 1), JSON.stringify({ historyMode: 'interval', historyBucketMinutes: 7 }))
    localStorage.setItem(key(1, 2), JSON.stringify({ historyMode: 'count', historyBarCount: 72 }))
    const wrapper = mountView()
    await flushPromises()
    expect(api.getObservationOverview.mock.lastCall![0].bucket_minutes).toBe(7)
    auth.workspace.id = 2
    await flushPromises()
    expect(api.getObservationOverview.mock.lastCall![0].bucket_count).toBe(72)
    expect(api.getObservationOverview.mock.lastCall![0]).not.toHaveProperty('bucket_minutes')
    auth.user.id = 2
    await flushPromises()
    expect(api.getObservationOverview.mock.lastCall![0]).not.toHaveProperty('bucket_count')
    expect(api.getObservationOverview.mock.lastCall![0]).not.toHaveProperty('bucket_minutes')
    auth.user.id = 1
    await flushPromises()
    expect(api.getObservationOverview.mock.lastCall![0].bucket_count).toBe(72)
    wrapper.unmount()
    const reloaded = mountView()
    await flushPromises()
    expect(api.getObservationOverview.mock.lastCall![0].bucket_count).toBe(72)
    reloaded.unmount()
  })

  it.each(['admin', 'vendor', 'user'])('applies real history requests only after valid settings for %s', async role => {
    stores.auth.user.role = role
    const wrapper = mountView()
    await flushPromises()
    wrapper.getComponent('[data-testid="pref-history-mode"]').vm.$emit('update:modelValue', 'interval')
    await flushPromises()
    await wrapper.get('[data-testid="pref-history-minutes"]').setValue('0')
    await wrapper.get('form').trigger('submit')
    expect(api.getObservationOverview).toHaveBeenCalledTimes(1)
    await wrapper.get('[data-testid="pref-history-minutes"]').setValue('30')
    await wrapper.get('[data-testid="open-bucket"]').trigger('click')
    expect(wrapper.find('[data-testid="bucket-detail"]').exists()).toBe(true)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.getObservationOverview).toHaveBeenCalledTimes(2)
    expect(api.getObservationOverview.mock.lastCall![0]).toMatchObject({ bucket_minutes: 30 })
    expect(wrapper.find('[data-testid="bucket-detail"]').exists()).toBe(false)
    wrapper.getComponent('[data-testid="pref-history-mode"]').vm.$emit('update:modelValue', 'count')
    await flushPromises()
    await wrapper.get('[data-testid="pref-history-count"]').setValue('48')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.getObservationOverview.mock.lastCall![0]).toMatchObject({ bucket_count: 48 })
    expect(api.getObservationOverview.mock.lastCall![0]).not.toHaveProperty('bucket_minutes')
    wrapper.getComponent('[data-testid="pref-history-mode"]').vm.$emit('update:modelValue', 'auto')
    await flushPromises()
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.getObservationOverview.mock.lastCall![0]).not.toHaveProperty('bucket_minutes')
    expect(api.getObservationOverview.mock.lastCall![0]).not.toHaveProperty('bucket_count')
    wrapper.unmount()
  })

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
  it('uses the site refresh interval and honors the browser auto-refresh preference', async () => {
    api.getObservationOverview.mockResolvedValue({ ...snapshot(), refresh_interval_seconds: 120 })
    const wrapper = mountView()
    await flushPromises()
    await vi.advanceTimersByTimeAsync(60_000)
    expect(api.getObservationOverview).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(60_000)
    expect(api.getObservationOverview).toHaveBeenCalledTimes(2)
    await wrapper.get('[data-testid="pref-auto-refresh"]').trigger('click')
    await vi.advanceTimersByTimeAsync(240_000)
    expect(api.getObservationOverview).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })
  it('uses the actual card layout rather than the obsolete list preference', async () => {
    localStorage.setItem('sub2api:observation:v2:1:admin:1:layout', 'list')
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.findComponent(cards).props('layout')).toBe('cards')
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
