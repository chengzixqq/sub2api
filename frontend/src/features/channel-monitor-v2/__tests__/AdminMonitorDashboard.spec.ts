import { flushPromises, shallowMount } from '@vue/test-utils'
import { reactive } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import Select from '@/components/common/Select.vue'
import AdminMonitorDashboard from '../AdminMonitorDashboard.vue'
import MonitorOverviewHero from '../MonitorOverviewHero.vue'
import MonitorViewSwitcher from '../MonitorViewSwitcher.vue'
import ObservationCards from '../ObservationCards.vue'
import ObservationDetailDrawer from '../ObservationDetailDrawer.vue'
import MonitorErrorBreakdown from '../MonitorErrorBreakdown.vue'
import FilterMultiSelect from '../FilterMultiSelect.vue'
import type { ObservationChannel, ObservationOverview } from '@/api/channelMonitorV2'

const api = vi.hoisted(() => ({ getObservationOverview: vi.fn(), getErrors: vi.fn() }))
vi.mock('@/api/channelMonitorV2', () => api)
const auth = reactive({ isOwner: true, user: { id: 1, role: 'admin' }, workspace: null as { id: number } | null })
vi.mock('@/stores/auth', () => ({ useAuthStore: () => auth }))
const translated = { 'channelMonitorV2.ranges.24h': '24h' }
vi.mock('vue-i18n', async importOriginal => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => translated[key as keyof typeof translated] || key, te: (key: string) => key in translated, locale: { value: 'en' } }) }))
const metric = { sample_state: 'sufficient', request_count: 100, success_requests: 95, channel_errors: 5, reliability_rate: .95, success_rate: .95, cache_rate: .4, ttft: { p50_ms: 200, p95_ms: 400, sample_count: 100, avg_ms: 250 }, duration: { p50_ms: 1000, p95_ms: 1200, sample_count: 100, avg_ms: 1100 } } as const
function item(source: 'compact' | 'legacy', id: number): ObservationChannel { return { source, platform: 'anthropic', group_id: id, group_name: 'Channel ' + id, metrics: { ...metric }, health: { reliability: 'healthy', latency: 'healthy' }, models: [], buckets: [] } }
function snapshot(source: ObservationOverview['source'] = 'compact'): ObservationOverview { return { contract_version: 2, source, mode: 'live', items: [item(source === 'legacy' ? 'legacy' : 'compact', 1)], dimensions: { platforms: [], groups: [], models: [] }, coverage: { requested_start: '2026-09-20T00:00:00Z', requested_end: '2026-09-21T00:00:00Z', coverage_start: '2026-09-20T00:00:00Z', data_through: '2026-09-21T00:00:00Z', computed_at: '2026-09-21T00:00:00Z', aggregation_lag_seconds: 0, coverage_complete: true, state: 'complete', bucket_seconds: 3600, detail_retention_hours: 24, unsupported_protocols: [] } } }

describe('admin monitor dashboard', () => {
  beforeEach(() => { localStorage.clear(); api.getObservationOverview.mockReset().mockResolvedValue(snapshot()); api.getErrors.mockReset().mockResolvedValue({ items: [] }); auth.isOwner = true; auth.user.id = 1; auth.user.role = 'admin'; auth.workspace = null })
  afterEach(() => localStorage.clear())

  it('uses the saved default range only when no explicit filter is supplied', async () => {
    localStorage.setItem('admin-monitor:1:admin:owner:presentation', JSON.stringify({ defaultRange: '7d' }))
    const savedDefault = shallowMount(AdminMonitorDashboard)
    await flushPromises()
    expect(savedDefault.getComponent(Select).props('modelValue')).toBe('7d')
    expect(api.getObservationOverview.mock.calls.at(-1)?.[0].range).toBe('7d')
    savedDefault.unmount()

    const explicit = shallowMount(AdminMonitorDashboard, { props: { filter: { range: '24h', platforms: [], groupIds: [], models: [] } } })
    await flushPromises()
    expect(explicit.getComponent(Select).props('modelValue')).toBe('24h')
    expect(api.getObservationOverview.mock.calls.at(-1)?.[0].range).toBe('24h')
    explicit.unmount()
  })

  it('keeps the preview range fixed at 24h despite a different saved default', async () => {
    localStorage.setItem('admin-monitor:1:admin:owner:presentation', JSON.stringify({ defaultRange: '7d' }))
    const wrapper = shallowMount(AdminMonitorDashboard, { props: { preview: true, previewOverview: snapshot() } })
    await flushPromises()
    expect(wrapper.getComponent(Select).props('modelValue')).toBe('24h')
    expect(wrapper.getComponent(Select).props('disabled')).toBe(true)
    expect(wrapper.text()).toContain('Preview uses a fixed 24h fixture window')
    expect(api.getObservationOverview).not.toHaveBeenCalled()
    expect(api.getErrors).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it.each(['account', 'workspace'] as const)('resets active view, range and filters across %s changes with equal defaults', async identityPart => {
    const wrapper = shallowMount(AdminMonitorDashboard)
    await flushPromises()
    wrapper.getComponent(Select).vm.$emit('update:modelValue', '90m')
    wrapper.getComponent(MonitorViewSwitcher).vm.$emit('update:modelValue', 'advanced')
    const selectors = wrapper.findAllComponents(FilterMultiSelect)
    selectors[0].vm.$emit('update:modelValue', ['openai'])
    selectors[1].vm.$emit('update:modelValue', ['2'])
    selectors[2].vm.$emit('update:modelValue', ['private-model'])
    await flushPromises()
    expect(wrapper.getComponent(Select).props('modelValue')).toBe('90m')
    expect(wrapper.getComponent(MonitorViewSwitcher).props('modelValue')).toBe('advanced')
    api.getObservationOverview.mockClear()

    if (identityPart === 'account') auth.user.id = 2
    else auth.workspace = { id: 8 }
    await flushPromises()
    expect(wrapper.getComponent(Select).props('modelValue')).toBe('24h')
    expect(wrapper.getComponent(MonitorViewSwitcher).props('modelValue')).toBe('cards')
    expect(wrapper.findAllComponents(FilterMultiSelect).map(select => select.props('modelValue'))).toEqual([[], [], []])
    expect(api.getObservationOverview).toHaveBeenCalledTimes(1)
    expect(api.getObservationOverview.mock.calls[0][0]).toEqual({ range: '24h', platforms: [], groupIds: [], models: [] })
    wrapper.unmount()
  })

  it('loads one overview on mount and requests legacy diagnostics only after advanced opens', async () => {
    api.getObservationOverview.mockResolvedValue(snapshot('legacy'))
    const wrapper = shallowMount(AdminMonitorDashboard)
    await flushPromises()
    expect(api.getObservationOverview).toHaveBeenCalledTimes(1)
    expect(api.getErrors).not.toHaveBeenCalled()
    wrapper.getComponent(MonitorViewSwitcher).vm.$emit('update:modelValue', 'advanced')
    await flushPromises()
    expect(api.getErrors).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('passes owner permission to detail and revokes it when the workspace identity changes', async () => {
    const wrapper = shallowMount(AdminMonitorDashboard)
    await flushPromises()
    wrapper.getComponent(ObservationCards).vm.$emit('detail', item('compact', 1))
    await flushPromises()
    expect(wrapper.getComponent(ObservationDetailDrawer).props('show')).toBe(true)
    expect(wrapper.getComponent(ObservationDetailDrawer).props('owner')).toBe(true)
    auth.isOwner = false; auth.user.role = 'vendor'; auth.workspace = { id: 8 }
    await flushPromises()
    expect(wrapper.getComponent(ObservationDetailDrawer).props('show')).toBe(false)
    expect(wrapper.getComponent(ObservationDetailDrawer).props('owner')).toBe(false)
    wrapper.unmount()
  })

  it('isolates local preview from all network requests and account drilldown', async () => {
    const wrapper = shallowMount(AdminMonitorDashboard, { props: { preview: true, previewOverview: snapshot() } })
    await flushPromises()
    wrapper.getComponent(MonitorViewSwitcher).vm.$emit('update:modelValue', 'advanced')
    await flushPromises()
    expect(api.getObservationOverview).not.toHaveBeenCalled()
    expect(api.getErrors).not.toHaveBeenCalled()
    expect(wrapper.getComponent(ObservationDetailDrawer).props('owner')).toBe(false)
    wrapper.unmount()
  })

  it('keeps mixed-source request totals separate and a probe-only row out of traffic health', async () => {
    const data = snapshot('mixed')
    data.items.push(item('legacy', 2))
    data.items[1].metrics.request_count = 25
    data.items.push({ ...item('compact', 3), metrics: { ...metric, sample_state: 'no_samples', request_count: 0, success_requests: 0, channel_errors: 0 }, probe: { status: 'healthy', consecutive_failures: 0 }, health: { reliability: 'unknown', latency: 'unknown' } })
    const wrapper = shallowMount(AdminMonitorDashboard, { props: { preview: true, previewOverview: data } })
    await flushPromises()
    const stats = wrapper.getComponent(MonitorOverviewHero).props('stats')
    expect(stats.find(stat => stat.label.startsWith('Requests'))?.value).toBe('100 / 25')
    expect(stats.find(stat => stat.label === 'Healthy / warning / critical')?.value).toBe('2 / 0 / 0')
    wrapper.unmount()
  })
  it('hides stale traffic headlines when coverage is unavailable', async () => {
    const data = snapshot()
    data.coverage.state = 'unavailable'
    data.coverage.coverage_complete = false
    const wrapper = shallowMount(AdminMonitorDashboard, { props: { preview: true, previewOverview: data } })
    const stats = wrapper.getComponent(MonitorOverviewHero).props('stats')
    expect(stats.slice(0, 4).map(stat => stat.value)).toEqual(['—', '—', '—', '—'])
    wrapper.unmount()
  })

  it.each([0, 1, 2])('discards late diagnostics when filter %s changes', async (filterIndex) => {
    api.getObservationOverview.mockResolvedValue(snapshot('legacy'))
    let oldResult!: (value: { items: Array<{ category: string; count: number; rate: number }> }) => void
    api.getErrors.mockImplementationOnce(() => new Promise(resolve => { oldResult = resolve }))
    const wrapper = shallowMount(AdminMonitorDashboard)
    await flushPromises()
    wrapper.getComponent(MonitorViewSwitcher).vm.$emit('update:modelValue', 'advanced')
    await flushPromises()
    const priorSignal = api.getErrors.mock.calls[0][2] as AbortSignal
    const priorFilter = api.getErrors.mock.calls[0][0]
    const values = [['openai'], ['2'], ['gpt-5']][filterIndex]
    wrapper.findAllComponents(FilterMultiSelect)[filterIndex].vm.$emit('update:modelValue', values)
    await flushPromises()
    expect(priorSignal.aborted).toBe(true)
    expect(priorFilter).toEqual({ range: '24h', platforms: [], groupIds: [], models: [] })
    oldResult({ items: [{ category: 'old_private_result', count: 99, rate: 1 }] })
    await flushPromises()
    expect(wrapper.getComponent(MonitorErrorBreakdown).props('items')).toEqual([])
    wrapper.unmount()
  })

})
