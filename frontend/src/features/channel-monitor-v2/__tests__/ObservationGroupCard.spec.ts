import { shallowMount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import ObservationGroupCard from '../ObservationGroupCard.vue'
import type { ObservationChannel, ObservationOverview } from '@/api/channelMonitorV2'

vi.mock('vue-i18n', async importOriginal => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key === 'channelMonitorV2.unified.gaps.collector_stale' ? 'Collector heartbeat delayed' : key, te: (key: string) => key === 'channelMonitorV2.unified.gaps.collector_stale', locale: { value: 'en' } }) }))
const metrics = { sample_state: 'sufficient', request_count: 12, channel_errors: 1, attempt_count: 14, reliability_rate: .9, error_categories: { timeout: 1 }, ttft: { p50_ms: 300 }, cache_rate: null } as ObservationChannel['metrics']
const item = { platform: 'anthropic', group_id: 1, group_name: 'Visible channel', metrics, health: { reliability: 'healthy', latency: 'healthy' }, buckets: [], models: [] } as ObservationChannel
const coverage = { state: 'partial', aggregation_lag_seconds: 180, gap_reasons: ['collector_stale'] } as ObservationOverview['coverage']

describe('admin observation card evidence', () => {
  it('shows actual lag, gaps, source fallback and recent error categories', () => {
    const wrapper = shallowMount(ObservationGroupCard, { props: { item, coverage, admin: true, source: 'terminal_v1' } })
    const evidence = wrapper.get('[data-testid="card-data-quality"]').text()
    expect(evidence).toContain('180s')
    expect(evidence).toContain('Collector heartbeat delayed')
    expect(evidence).toContain('timeout · 1')
    expect(wrapper.text()).toContain('terminal_v1')
    wrapper.unmount()
  })
  it('does not fabricate zero errors when there are no real traffic samples', () => {
    const empty = { ...item, metrics: { ...metrics, sample_state: 'no_samples' as const, request_count: 0, channel_errors: 0, attempt_count: 0 } }
    const wrapper = shallowMount(ObservationGroupCard, { props: { item: empty, coverage, admin: true } })
    const counts = wrapper.findAll('dl')[1].findAll('dd').map(cell => cell.text())
    expect(counts).toEqual(['—', '—', '—'])
    expect(wrapper.get('[data-testid="card-data-quality"]').text()).toContain('states.no_samples')
    wrapper.unmount()
  })
  it('uses explicit traffic metrics and keeps admin details out of a user card', () => {
    const wrapper = shallowMount(ObservationGroupCard, { props: { item: { ...item, traffic: { ...metrics, channel_errors: 0, error_categories: {} } }, coverage, admin: true } })
    expect(wrapper.findAll('dl')[1].findAll('dd')[1].text()).toBe('0')
    expect(wrapper.get('[data-testid="card-data-quality"]').text()).toContain('No observed channel errors')
    wrapper.unmount()
    const user = shallowMount(ObservationGroupCard, { props: { item, coverage } })
    expect(user.find('[data-testid="card-data-quality"]').exists()).toBe(false)
    user.unmount()
  })
})
