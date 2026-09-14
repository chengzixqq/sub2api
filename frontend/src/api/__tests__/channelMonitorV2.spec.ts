import { afterEach, describe, expect, it, vi } from 'vitest'
import { apiClient } from '../client'
import { getMatrix, getObservationOverview, updateObservationConfig, repeatedArrayParamsSerializer } from '../channelMonitorV2'

afterEach(() => vi.restoreAllMocks())

describe('channel monitor V2 query serialization', () => {
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
