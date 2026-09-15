import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import ObservationTimeline from '../ObservationTimeline.vue'

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
    locale: { value: 'zh-CN' },
    t: (key: string, params?: Record<string, unknown>) => {
      const labels: Record<string, string> = {
        'channelMonitorV2.observation.states.healthy': '正常',
        'channelMonitorV2.observation.states.sufficient': '样本充足',
        'channelMonitorV2.observation.states.missing': '数据缺失',
        'channelMonitorV2.observation.requests': '请求数',
        'channelMonitorV2.observation.errors': '渠道错误',
        'channelMonitorV2.observation.attempts': '上游尝试',
        'channelMonitorV2.observation.firstOutput': '首字延迟',
        'channelMonitorV2.observation.cache': '缓存命中率',
        'channelMonitorV2.observation.history': '历史状态',
      }
      return labels[key] || String(params?.value ?? key)
    },
    }),
  }
})

describe('ObservationTimeline', () => {
  it('shows the selected period details on mouse hover and removes them on leave', async () => {
    const wrapper = mount(ObservationTimeline, {
      props: {
        admin: true,
        coverage: {
          state: 'complete',
          requested_start: '2026-09-15T17:00:00Z',
          requested_end: '2026-09-15T17:01:00Z',
          data_through: '2026-09-15T17:01:00Z',
          bucket_seconds: 60,
        } as never,
        buckets: [{
          bucket_start: '2026-09-15T17:00:00Z',
          metrics: {
            request_count: 12,
            channel_errors: 2,
            attempt_count: 15,
            reliability_rate: 10 / 12,
            sample_state: 'sufficient',
            cache_rate: 0.75,
            ttft: { sample_count: 10, p50_ms: 480, p95_ms: 900, avg_ms: 520 },
            duration: { sample_count: 12, p50_ms: 1200, p95_ms: 2000, avg_ms: 1300 },
          },
          health: { reliability: 'healthy', latency: 'healthy' },
        }],
      },
    })
    const block = wrapper.find('button')
    expect(block.exists()).toBe(true)
    await block.trigger('mouseenter')
    expect(wrapper.find('[role="tooltip"]').text()).toContain('请求数 12')
    expect(wrapper.find('[role="tooltip"]').text()).toContain('上游尝试 15')
    await block.trigger('mouseleave')
    expect(wrapper.find('[role="tooltip"]').exists()).toBe(false)
  })
})
