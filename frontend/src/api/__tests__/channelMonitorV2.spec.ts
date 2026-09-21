import { afterEach, describe, expect, it, vi } from 'vitest'
import { apiClient } from '../client'
import { getMatrix, getObservationOverview, getMonitorAccountDetail, runProbeTarget, updateObservationConfig, repeatedArrayParamsSerializer, type ObservationMetrics } from '../channelMonitorV2'

afterEach(() => vi.restoreAllMocks())

describe('channel monitor V2 query serialization', () => {
  it('keeps unclassified legacy attempts separate from channel errors in the response contract', () => {
    const metrics: ObservationMetrics = {
      channel_errors: 0,
      unclassified_attempts: 2,
      success_rate: null,
      reliability_rate: null,
      sample_state: 'no_samples',
      cache_rate: null,
      ttft: { sample_count: 0, p50_ms: null, p95_ms: null, avg_ms: null },
      duration: { sample_count: 0, p50_ms: null, p95_ms: null, avg_ms: null },
    }

    expect(metrics).toMatchObject({ channel_errors: 0, unclassified_attempts: 2 })
  })

  it('only sends publication preview to an explicit owner request', async () => {
    const get = vi.spyOn(apiClient, 'get').mockResolvedValue({ data: {} })
    const filter = { range: '24h' as const, platforms: [], groupIds: [], models: [] }
    await getObservationOverview(filter, false, undefined, { endTime: '2026-09-18T00:00:00Z', preview: true })
    expect(get.mock.calls.at(-1)![1]?.params.preview).toBeUndefined()
    await getObservationOverview(filter, true, undefined, { endTime: '2026-09-18T00:00:00Z', preview: true })
    expect(get.mock.calls.at(-1)![1]?.params.preview).toBe(true)
  })
  it('uses the owner account detail contract and explicit probe idempotency header', async () => {
    const get = vi.spyOn(apiClient, 'get').mockResolvedValue({ data: { items: [], samples: [] } })
    const post = vi.spyOn(apiClient, 'post').mockResolvedValue({ data: { status: 'pending' } })
    await getMonitorAccountDetail(7, { range: '24h', platforms: [], groupIds: [], models: ['model'] })
    expect(get).toHaveBeenCalledWith('/admin/channel-monitor-v2/groups/7/accounts', expect.objectContaining({ params: { range: '24h', model: ['model'] } }))
    await runProbeTarget(9, 'test-idempotency-key-1')
    expect(post).toHaveBeenCalledWith('/admin/channel-monitor-v2/probe-targets/9/probe', undefined, { headers: { 'Idempotency-Key': 'test-idempotency-key-1' } })
  })
  it('freezes the observation query and requests a server-side user projection', async () => {
    const get = vi.spyOn(apiClient, 'get').mockResolvedValue({ data: { contract_version: 2 } })
    const controller = new AbortController()
    await getObservationOverview({ range: '7d', platforms: ['openai'], groupIds: [7], models: ['model'] }, true, controller.signal, { endTime: '2026-09-15T00:00:00Z', refresh: true, audience: 'user' })
    expect(get).toHaveBeenCalledWith('/admin/channel-monitor-v2/overview', expect.objectContaining({
      signal: controller.signal,
      params: expect.objectContaining({ end_time: '2026-09-15T00:00:00Z', refresh: true, audience: 'user', model: ['model'] }),
    }))
  })

  it('keeps observation policy separate from legacy health configuration', async () => {
    const put = vi.spyOn(apiClient, 'put').mockResolvedValue({ data: { enabled: false } })
    await updateObservationConfig({ enabled: false } as Parameters<typeof updateObservationConfig>[0])
    expect(put).toHaveBeenCalledWith('/admin/channel-monitor-v2/observation-config', { enabled: false })
  })
  it('uses repeated keys without bracket suffixes for array filters', () => {
    const query = repeatedArrayParamsSerializer({
      range: '90m',
      platform: ['openai', 'grok'],
      group_id: [1, 2],
      model: undefined,
      group_by: 'platform_group_model',
    })

    expect(query).toBe('range=90m&platform=openai&platform=grok&group_id=1&group_id=2&group_by=platform_group_model')
    expect(query).not.toContain('%5B%5D')
  })

  it('sends the matrix grouping with the shared filters', async () => {
    const get = vi.spyOn(apiClient, 'get').mockResolvedValue({
      data: { coverage: {}, group_by: 'platform_group', items: [] },
    })

    await getMatrix({ range: '24h', platforms: ['openai'], groupIds: [7], models: [] }, 'platform_group', true)

    expect(get).toHaveBeenCalledWith('/admin/channel-monitor-v2/matrix', expect.objectContaining({
      params: {
        range: '24h',
        platform: ['openai'],
        group_id: [7],
        model: undefined,
        group_by: 'platform_group',
      },
    }))
  })
})
