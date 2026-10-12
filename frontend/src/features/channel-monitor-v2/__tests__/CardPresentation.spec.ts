import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import type { ObservationChannel, ObservationOverview } from '@/api/channelMonitorV2'
import ObservationCards from '../ObservationCards.vue'
import ObservationCurrentStatus from '../ObservationCurrentStatus.vue'
import ObservationGroupCard from '../ObservationGroupCard.vue'
import ObservationMetrics from '../ObservationMetrics.vue'
import ObservationTimeline from '../ObservationTimeline.vue'

vi.mock('vue-i18n', async importOriginal => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key, te: () => false, locale: { value: 'en' } }),
}))

const metrics = {
  sample_state: 'sufficient', request_count: 121, channel_errors: 3, attempt_count: 124,
  reliability_rate: 0.975, success_rate: 0.975, cache_rate: 0.7,
  ttft: { p50_ms: 300, p95_ms: 600 }, duration: { p50_ms: 1500 },
  error_categories: { timeout: 3 },
} as ObservationChannel['metrics']
const coverage = {
  state: 'complete', requested_start: '2026-10-11T00:00:00Z', requested_end: '2026-10-11T00:01:00Z',
  data_through: '2026-10-11T00:01:00Z', bucket_seconds: 60, aggregation_lag_seconds: 5,
} as ObservationOverview['coverage']
const health: ObservationChannel['health'] = { reliability: 'healthy', latency: 'healthy' }
function channel(id: number, platform = 'openai'): ObservationChannel {
  return {
    group_id: id, platform, group_name: `Channel ${id}`, metrics, health, models: [],
    current_status: { state: 'healthy', source: 'probe', reason: 'probe_success' },
    buckets: [{ bucket_start: coverage.requested_start, metrics, health }],
  }
}
const global = { stubs: { teleport: true, Icon: true, PlatformIcon: true } }
const metricKeys = (wrapper: ReturnType<typeof mount>) => wrapper.findAll('dt').map(node => node.text().split('.').at(-1))

describe('monitor card presentation contracts', () => {
  it('groups cards by explicit platform and shows each platform count', () => {
    const overview = {
      contract_version: 2, source: 'compact', mode: 'live', coverage,
      dimensions: { platforms: [], groups: [], models: [] },
      items: [channel(1), channel(2, 'anthropic'), channel(3)],
    } as ObservationOverview
    const wrapper = mount(ObservationCards, { props: { overview, layout: 'cards' }, global })
    const anthropic = wrapper.get('section[aria-label="Anthropic"]')
    const openai = wrapper.get('section[aria-label="OpenAI"]')
    expect(anthropic.findAll('[data-group-id]')).toHaveLength(1)
    expect(openai.findAll('[data-group-id]')).toHaveLength(2)
    expect(anthropic.get('header').text()).toBe('Anthropic1')
    expect(openai.get('header').text()).toBe('OpenAI2')
    expect(wrapper.findAll('h2').map(node => node.text())).toEqual(['Anthropic', 'OpenAI'])
    wrapper.unmount()
  })

  it('changes only card metric order and chrome, preserving the default variant', () => {
    const card = mount(ObservationMetrics, { props: { metrics, health, variant: 'card' } })
    expect(metricKeys(card)).toEqual(['cache', 'reliability', 'firstOutput'])
    expect(card.classes()).toContain('card-metrics')
    expect(card.findAll('.card-metric-cell')).toHaveLength(3)
    expect(card.text()).toContain('P95 600ms')
    const standard = mount(ObservationMetrics, { props: { metrics, health } })
    expect(metricKeys(standard)).toEqual(['reliability', 'firstOutput', 'cache'])
    expect(standard.classes()).toContain('divide-x')
    expect(standard.find('.card-metric-cell').exists()).toBe(false)
    card.unmount()
    standard.unmount()
  })

  it('does not convert unavailable card metrics into healthy numbers', () => {
    const wrapper = mount(ObservationMetrics, { props: { metrics, health, variant: 'card', unavailable: true } })
    expect(wrapper.findAll('dd').map(node => node.text())).toEqual(['-', '-', '-'])
    expect(wrapper.text()).not.toContain('P95')
    wrapper.unmount()
  })

  it('keeps compact current evidence source while marking a retained snapshot stale', () => {
    const wrapper = mount(ObservationCurrentStatus, {
      props: { compact: true, stale: true, status: { state: 'healthy', source: 'probe', reason: 'probe_success', updated_at: coverage.requested_end } },
    })
    expect(wrapper.text()).toContain('unified.states.stale')
    expect(wrapper.text()).toContain('unified.sources.probe')
    expect(wrapper.text()).not.toContain('unified.states.healthy')
    expect(wrapper.text()).not.toContain('unified.sources.traffic')
    expect(wrapper.get('.badge').attributes('title')).toBe('probe_success')
    expect(wrapper.get('time').attributes('datetime')).toBe(coverage.requested_end)
    wrapper.unmount()
  })

  it('keeps operational counts and error details private in cards and timeline tooltips', async () => {
    const user = mount(ObservationGroupCard, { props: { item: channel(1), coverage }, global })
    expect(user.find('[data-testid="card-traffic-counts"]').exists()).toBe(false)
    expect(user.find('[data-testid="card-data-quality"]').exists()).toBe(false)
    await user.get('[data-testid="observation-timeline"] button').trigger('mouseenter')
    expect(user.get('[role="tooltip"]').text()).not.toContain('observation.requests')
    expect(user.get('[role="tooltip"]').text()).not.toContain('observation.attempts')
    const admin = mount(ObservationGroupCard, { props: { item: channel(1), coverage, admin: true }, global })
    expect(admin.get('[data-testid="card-traffic-counts"]').findAll('dd').map(node => node.text())).toEqual(['121', '3', '124'])
    expect(admin.get('[data-testid="card-data-quality"]').text()).toContain('timeout · 3')
    user.unmount()
    admin.unmount()
  })

  it('keeps every 24-hour bucket and selection behavior in the card timeline', async () => {
    const day = { ...coverage, requested_end: '2026-10-12T00:00:00Z', data_through: '2026-10-12T00:00:00Z', bucket_seconds: 300 }
    const wrapper = mount(ObservationTimeline, { props: { coverage: day, buckets: [], variant: 'card' } })
    expect(wrapper.findAll('button')).toHaveLength(288)
    const timeline = wrapper.get('[data-testid="observation-timeline"]')
    expect(timeline.attributes('style')).toContain('repeat(288, minmax(0, 1fr))')
    expect(timeline.attributes('style')).toContain('gap: 0')
    const first = wrapper.get('button')
    expect(first.classes()).toContain('rounded-[5px]')
    expect(first.classes()).toContain('border-dashed')
    await first.trigger('click')
    expect(wrapper.emitted('select')).toEqual([[{ start: '2026-10-11T00:00:00.000Z', bucket: null }]])
    wrapper.unmount()
  })

  it('uses separated card segments without changing the default timeline style', () => {
    const card = mount(ObservationTimeline, { props: { coverage, buckets: channel(1).buckets, variant: 'card' } })
    expect(card.get('[data-testid="observation-timeline"]').attributes('style')).toContain('gap: 4px')
    expect(card.get('button').classes()).toContain('bg-emerald-500')
    const standard = mount(ObservationTimeline, { props: { coverage, buckets: channel(1).buckets } })
    expect(standard.get('[data-testid="observation-timeline"]').attributes('style')).toContain('gap: 2px')
    expect(standard.get('button').classes()).toContain('h-6')
    expect(standard.get('button').classes()).not.toContain('rounded-[5px]')
    card.unmount()
    standard.unmount()
  })
})
