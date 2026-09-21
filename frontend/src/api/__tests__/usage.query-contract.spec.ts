import { beforeEach, describe, expect, it, vi } from 'vitest'
const { get } = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get } }))
import * as usage from '@/api/usage'
import * as adminUsage from '@/api/admin/usage'
import * as dashboard from '@/api/admin/dashboard'
import { listErrorLogs } from '@/api/admin/ops'

describe('usage minute query transport', () => {
  beforeEach(() => get.mockReset().mockResolvedValue({ data: {} }))
  const params = { start_time: '2026-09-14T01:02:00Z', end_time: '2026-09-14T01:03:00Z', timezone: 'Asia/Shanghai', model: 'test', billing_mode: 'token', force_refresh: true }
  it('forwards the identical full query and cancellation to every statistics endpoint', async () => {
    const options = { signal: new AbortController().signal }
    await usage.getStats(params, undefined, options)
    await usage.getDashboardModels(params, options)
    await usage.getDashboardSnapshotV2(params, options)
    await adminUsage.getStats(params, options)
    await dashboard.getModelStats(params, options)
    await dashboard.getSnapshotV2(params, options)
    await dashboard.getUserBreakdown(params, options)
    expect(get).toHaveBeenCalledTimes(7)
    for (const [, config] of get.mock.calls) {
      expect(config.params).toEqual(params)
      expect(config.signal).toBe(options.signal)
    }
  })
  it('preserves deferred count markers without fabricating a total', async () => {
    const response = { items: [], total: null, pages: null, page: 1, page_size: 20, total_exact: false, has_more: true }
    get.mockResolvedValue({ data: response })
    expect(await usage.query({ ...params, count_mode: 'deferred' })).toBe(response)
    expect(await adminUsage.list({ ...params, count_mode: 'deferred' })).toBe(response)
    expect(get.mock.calls.every(([, config]) => config.params.count_mode === 'deferred')).toBe(true)
  })
  it('passes signals to both error endpoints', async () => {
    const options = { signal: new AbortController().signal }
    await usage.listMyErrorRequests(params, options)
    await listErrorLogs(params, options)
    expect(get.mock.calls.every(([, config]) => config.signal === options.signal)).toBe(true)
  })
})
