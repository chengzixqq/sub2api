import { defineComponent, h } from 'vue'
import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import ObservationCards from '../ObservationCards.vue'
import type { ObservationOverview } from '@/api/channelMonitorV2'

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

const groupCardStub = defineComponent({
  name: 'ObservationGroupCard',
  props: ['item'],
  emits: ['bucket'],
  setup(props, { emit }) {
    return () => h('button', {
      'data-testid': `group-${props.item.group_id}`,
      onClick: () => emit('bucket', props.item, { start: '2026-09-15T17:00:00Z', bucket: null }),
    })
  },
})

const overview = {
  contract_version: 2,
  source: 'terminal_v1',
  mode: 'shadow',
  coverage: {
    state: 'complete',
    requested_start: '2026-09-15T17:00:00Z',
    requested_end: '2026-09-15T17:01:00Z',
    data_through: '2026-09-15T17:01:00Z',
    bucket_seconds: 60,
  },
  dimensions: { platforms: [], groups: [], models: [] },
  items: [{
    platform: 'openai',
    group_id: 7,
    group_name: 'Default',
    metrics: { reliability_rate: null, sample_state: 'no_samples', ttft: { p50_ms: null }, cache_rate: null },
    health: { reliability: 'unknown', latency: 'unknown' },
    buckets: [],
    models: [],
  }],
} as unknown as ObservationOverview

describe('ObservationCards events', () => {
  it('forwards a bucket selection from a group card', async () => {
    const wrapper = mount(ObservationCards, {
      props: { overview, layout: 'cards' },
      global: {
        stubs: {
          ObservationGroupCard: groupCardStub,
          Icon: true,
          PlatformIcon: true,
        },
      },
    })

    await wrapper.get('[data-testid="group-7"]').trigger('click')
    expect(wrapper.emitted('bucket')).toEqual([
      [overview.items[0], { start: '2026-09-15T17:00:00Z', bucket: null }],
    ])
  })
})
