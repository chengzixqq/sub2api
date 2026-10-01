import { describe, expect, it } from 'vitest'
import zh from '@/i18n/locales/zh/channelMonitorV2'
import en from '@/i18n/locales/en/channelMonitorV2'
import { monitorCategoryLabel, monitorOutcomeLabel, monitorSourceLabel } from '../monitorLabels'

function translator(messages: object) {
  const read = (key: string): unknown => key.split('.').reduce<unknown>((value, part) => value && typeof value === 'object' ? (value as Record<string, unknown>)[part] : undefined, messages)
  return { t: (key: string) => String(read(key) ?? key), te: (key: string) => typeof read(key) === 'string' }
}

describe('monitor labels', () => {
  it('uses translated source names and keeps unknown identifiers readable', () => {
    const { t, te } = translator(zh)
    expect(monitorSourceLabel('compact', t, te)).toBe('精简聚合数据')
    expect(monitorSourceLabel('legacy', t, te)).toBe('旧版数据')
    expect(monitorSourceLabel('mixed', t, te)).toBe('混合数据源')
    expect(monitorSourceLabel('terminal_v1', t, te)).toBe('终态数据 V1')
    expect(monitorSourceLabel('new_source', t, te)).toBe('new_source')
    expect(monitorSourceLabel(undefined, t, te)).toBe('—')
  })

  it('translates actual transport categories and completed HTTP outcomes', () => {
    const { t, te } = translator(zh)
    for (const category of ['transport_error', 'upstream_auth', 'upstream_balance', 'request_too_large', 'upstream_capacity', 'upstream_error', 'upstream_http', 'client_request', 'incomplete_terminal', 'empty_output']) {
      expect(monitorCategoryLabel(category, t, te)).not.toBe(category)
    }
    expect(monitorCategoryLabel('compact · upstream_auth', t, te)).toBe('精简聚合数据 · 上游认证失败')
    expect(monitorOutcomeLabel('http_complete', t, te)).toBe('HTTP 请求完成')
  })

  it('has Chinese and English labels for collector gaps emitted by the backend', () => {
    for (const messages of [zh, en]) {
      const { te } = translator(messages)
      for (const reason of ['capacity', 'queue_loss', 'shutdown_loss', 'heartbeat_failed', 'gap_write_failed', 'terminal_conflict']) {
        expect(te(`channelMonitorV2.unified.gaps.${reason}`)).toBe(true)
      }
    }
  })

  it('covers the probe and quota statuses returned by the API in both languages', () => {
    for (const messages of [zh, en]) {
      const { te } = translator(messages)
      for (const status of ['healthy', 'warning', 'critical', 'error', 'pending', 'available', 'unknown', 'stale', 'unavailable']) {
        expect(te(`channelMonitorV2.unified.states.${status}`)).toBe(true)
      }
    }
    // This state means that quota snapshots were synchronized; it is not a balance assertion.
    expect(zh.channelMonitorV2.unified.states.available).toBe('同步正常')
  })
})
