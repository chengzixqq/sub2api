import { beforeEach, describe, expect, it, vi } from 'vitest'

const { post } = vi.hoisted(() => ({
  post: vi.fn()
}))

vi.mock('@/api/client', () => ({
  apiClient: { post }
}))

import { getDashboardApiKeysUsage } from '@/api/usage'

describe('user API key usage batching', () => {
  beforeEach(() => {
    post.mockReset()
  })

  it('keeps a full 1000-row page in one request', async () => {
    post.mockImplementation(async (_url: string, payload: { api_key_ids: number[] }) => ({
      data: {
        stats: Object.fromEntries(
          payload.api_key_ids.map((id) => [
            String(id),
            { api_key_id: id, today_actual_cost: id / 100, total_actual_cost: id }
          ])
        )
      }
    }))

    const ids = Array.from({ length: 1000 }, (_, index) => index + 1)
    const response = await getDashboardApiKeysUsage(ids)

    expect(post).toHaveBeenCalledOnce()
    expect(post.mock.calls[0][1].api_key_ids).toEqual(ids)
    expect(Object.keys(response.stats)).toHaveLength(1000)
    expect(response.stats['1000']).toEqual({
      api_key_id: 1000,
      today_actual_cost: 10,
      total_actual_cost: 1000
    })
  })

  it('deduplicates IDs and drops invalid values before requesting usage', async () => {
    post.mockResolvedValue({ data: { stats: {} } })

    await getDashboardApiKeysUsage([7, 7, 0, -1, Number.NaN, 8])

    expect(post).toHaveBeenCalledOnce()
    expect(post.mock.calls[0][1]).toEqual({ api_key_ids: [7, 8] })
  })

  it('fails the whole operation when any batch fails', async () => {
    post
      .mockResolvedValueOnce({ data: { stats: { '1': { api_key_id: 1 } } } })
      .mockRejectedValueOnce(new Error('usage unavailable'))

    const ids = Array.from({ length: 1250 }, (_, index) => index + 1)
    await expect(getDashboardApiKeysUsage(ids)).rejects.toThrow('usage unavailable')
    expect(post).toHaveBeenCalledTimes(2)
    expect(post.mock.calls[0][1].api_key_ids).toHaveLength(1000)
    expect(post.mock.calls[1][1].api_key_ids).toHaveLength(250)
  })

  it('does not call the endpoint for an empty ID list', async () => {
    await expect(getDashboardApiKeysUsage([])).resolves.toEqual({ stats: {} })
    expect(post).not.toHaveBeenCalled()
  })
})
