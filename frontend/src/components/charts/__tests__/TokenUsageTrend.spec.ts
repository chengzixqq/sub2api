import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { reactive, nextTick } from 'vue'

import TokenUsageTrend from '../TokenUsageTrend.vue'

const messages: Record<string, string> = reactive({
  'admin.dashboard.tokenUsageTrend': 'Token Usage Trend',
  'admin.dashboard.noDataAvailable': 'No data available',
  'usage.tokenTrend.input': 'Input',
  'usage.tokenTrend.output': 'Output',
  'usage.tokenTrend.cacheCreation': 'Cache Creation',
  'usage.tokenTrend.cacheRead': 'Cache Read',
  'usage.tokenTrend.cacheHitRate': 'Cache Hit Rate',
  'usage.tokenTrend.actualCost': 'Actual',
  'usage.tokenTrend.standardCost': 'Standard',
})

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => messages[key] ?? key,
    }),
  }
})

vi.mock('vue-chartjs', () => ({
  Line: {
    name: 'Line',
    props: ['data', 'options'],
    template: '<div class="chart-data">{{ JSON.stringify(data) }}</div>',
  },
}))

describe('TokenUsageTrend', () => {
  it('reacts to translated legends and tooltip costs without changing metrics', async () => {
    const wrapper = mount(TokenUsageTrend, { props: { trendData: [{
      date: '2026-09-14', requests: 1, input_tokens: 10, output_tokens: 20,
      cache_creation_tokens: 30, cache_read_tokens: 40, cost: 2, actual_cost: 1,
    }] } })
    const original = { ...messages }
    try {
      messages['usage.tokenTrend.input'] = 'Translated input'
      messages['usage.tokenTrend.actualCost'] = 'Translated actual'
      await nextTick()
      const chart = wrapper.findComponent({ name: 'Line' })
      const data = JSON.parse(wrapper.get('.chart-data').text())
      expect(data.datasets[0].label).toBe('Translated input')
      expect(data.datasets[0].data).toEqual([10])
      expect(chart.props('options').plugins.tooltip.callbacks.footer([{ dataIndex: 0 }]))
        .toBe('Translated actual: $1.00 | Standard: $2.00')
    } finally {
      Object.assign(messages, original)
      wrapper.unmount()
    }
  })
  it('calculates cache hit rate against all prompt tokens', () => {
    const wrapper = mount(TokenUsageTrend, {
      props: {
        trendData: [
          {
            date: '2026-05-08',
            requests: 1,
            input_tokens: 500,
            output_tokens: 100,
            cache_creation_tokens: 0,
            cache_read_tokens: 1500,
            cost: 0.01,
            actual_cost: 0.005,
          },
        ],
      },
      global: {
        stubs: {
          LoadingSpinner: true,
        },
      },
    })

    const chartData = JSON.parse(wrapper.find('.chart-data').text())
    const hitRateDataset = chartData.datasets.find(
      (ds: any) => ds.label === 'Cache Hit Rate'
    )
    // Hit rate = 1500 / (500 + 1500 + 0) * 100 = 75%
    expect(hitRateDataset.data[0]).toBe(75)
  })

  it('returns 0 hit rate when all prompt tokens are zero', () => {
    const wrapper = mount(TokenUsageTrend, {
      props: {
        trendData: [
          {
            date: '2026-05-08',
            requests: 0,
            input_tokens: 0,
            output_tokens: 0,
            cache_creation_tokens: 0,
            cache_read_tokens: 0,
            cost: 0,
            actual_cost: 0,
          },
        ],
      },
      global: {
        stubs: {
          LoadingSpinner: true,
        },
      },
    })

    const chartData = JSON.parse(wrapper.find('.chart-data').text())
    const hitRateDataset = chartData.datasets.find(
      (ds: any) => ds.label === 'Cache Hit Rate'
    )
    expect(hitRateDataset.data[0]).toBe(0)
  })

  it('includes cache_creation_tokens in denominator for Anthropic models', () => {
    const wrapper = mount(TokenUsageTrend, {
      props: {
        trendData: [
          {
            date: '2026-05-08',
            requests: 1,
            input_tokens: 200,
            output_tokens: 50,
            cache_creation_tokens: 300,
            cache_read_tokens: 500,
            cost: 0.02,
            actual_cost: 0.01,
          },
        ],
      },
      global: {
        stubs: {
          LoadingSpinner: true,
        },
      },
    })

    const chartData = JSON.parse(wrapper.find('.chart-data').text())
    const hitRateDataset = chartData.datasets.find(
      (ds: any) => ds.label === 'Cache Hit Rate'
    )
    // Hit rate = 500 / (200 + 500 + 300) * 100 = 50%
    expect(hitRateDataset.data[0]).toBe(50)
  })
})
