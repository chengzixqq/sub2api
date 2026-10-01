import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import { createI18n } from 'vue-i18n'
import { baseCompile } from '@intlify/message-compiler'
import { describe, expect, it } from 'vitest'
import type { ObservationOverview } from '@/api/channelMonitorV2'
import en from '@/i18n/locales/en/channelMonitorV2'
import zh from '@/i18n/locales/zh/channelMonitorV2'
import MonitorEvidenceStatus from '../MonitorEvidenceStatus.vue'

function compiledMessages<T extends object>(messages: T): T {
  return Object.fromEntries(Object.entries(messages).map(([key, value]) => [key,
    typeof value === 'string' ? new Function(`return ${baseCompile(value).code}`)() : compiledMessages(value),
  ])) as T
}
const messages = { zh: compiledMessages(zh), en: compiledMessages(en) }

function snapshot(state: ObservationOverview['coverage']['state'] = 'partial'): ObservationOverview {
  return {
    contract_version: 2, source: 'mixed', mode: 'live', items: [], dimensions: { platforms: [], groups: [], models: [] },
    coverage: {
      state, requested_start: '2026-09-20T00:00:00Z', requested_end: '2026-09-21T00:00:00Z',
      coverage_start: '2026-09-20T01:00:00Z', data_through: '2026-09-20T23:00:00Z', computed_at: '2026-09-21T00:00:00Z',
      aggregation_lag_seconds: 3600, coverage_complete: false, bucket_seconds: 3600, detail_retention_hours: 24,
      unsupported_protocols: [], collector_state: 'backlogged', gap_reasons: ['collector_backlog', 'legacy_window_unavailable', 'unsupported_protocol', 'queue_overflow', 'capacity', 'queue_loss', 'shutdown_loss', 'heartbeat_failed', 'gap_write_failed', 'terminal_conflict'],
    },
  }
}

describe('monitor evidence status', () => {
  it('reports coverage state, actual coverage window, lag and every gap without inventing a coverage rate', () => {
    const i18n = createI18n({ legacy: false, locale: 'zh', messages })
    const wrapper = mount(MonitorEvidenceStatus, { props: { overview: snapshot() }, global: { plugins: [i18n] } })
    expect(wrapper.text()).toContain('部分覆盖')
    expect(wrapper.text()).toContain('覆盖时段')
    expect(wrapper.text()).toContain(new Date('2026-09-20T01:00:00Z').toLocaleString('zh'))
    expect(wrapper.text()).toContain(new Date('2026-09-20T23:00:00Z').toLocaleString('zh'))
    expect(wrapper.text()).toContain('聚合延迟 3600 秒')
    expect(wrapper.text()).toContain('混合数据源')
    expect(wrapper.text()).not.toContain('mixed')
    expect(wrapper.text()).toContain('历史数据窗口不可用')
    expect(wrapper.text()).toContain('采集队列溢出')
    expect(wrapper.text()).toContain('采集容量不足')
    expect(wrapper.text()).toContain('采集队列丢失事件')
    expect(wrapper.text()).toContain('服务停止时仍有请求未完成')
    expect(wrapper.text()).toContain('采集心跳写入失败')
    expect(wrapper.text()).toContain('缺口记录写入失败')
    expect(wrapper.text()).toContain('终态记录冲突')
    expect(wrapper.text()).not.toContain('capacity')
    expect(wrapper.text()).not.toContain('queue_loss')
    expect(wrapper.text()).not.toContain('shutdown_loss')
    expect(wrapper.text()).not.toContain('heartbeat_failed')
    expect(wrapper.text()).not.toContain('gap_write_failed')
    expect(wrapper.text()).not.toContain('terminal_conflict')
    expect(wrapper.text()).not.toContain('%')
    wrapper.unmount()
  })

  it('keeps unavailable coverage distinct from missing traffic samples and reacts to locale changes', async () => {
    const data = snapshot('unavailable')
    data.coverage.coverage_start = '0001-01-01T00:00:00Z'
    data.coverage.data_through = '0001-01-01T00:00:00Z'
    const i18n = createI18n({ legacy: false, locale: 'zh', messages })
    const wrapper = mount(MonitorEvidenceStatus, { props: { overview: data, stale: true }, global: { plugins: [i18n] } })
    expect(wrapper.text()).toContain('暂不可用')
    expect(wrapper.text()).toContain('快照已过期')
    expect(wrapper.text()).not.toContain('暂无样本')
    expect(wrapper.text()).not.toContain('覆盖时段')
    i18n.global.locale.value = 'en'
    await nextTick()
    expect(wrapper.text()).toContain('Unavailable')
    expect(wrapper.text()).toContain('Stale snapshot')
    expect(wrapper.text()).toContain('Aggregation lag 3600s')
    expect(wrapper.text()).not.toContain('No traffic samples')
    wrapper.unmount()
  })

  it('translates collector states that can be absent or unavailable', () => {
    const data = snapshot()
    data.coverage.collector_state = 'unavailable'
    const i18n = createI18n({ legacy: false, locale: 'zh', messages })
    const wrapper = mount(MonitorEvidenceStatus, { props: { overview: data }, global: { plugins: [i18n] } })
    expect(wrapper.text()).toContain('采集不可用')
    expect(wrapper.text()).not.toContain('unavailable')
    wrapper.unmount()
  })
})
