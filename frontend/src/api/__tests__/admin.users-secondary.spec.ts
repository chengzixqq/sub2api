import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get, post } }))
import { getBatchUsersUsage } from '@/api/admin/dashboard'
import { getPlatformQuotas, getUserApiKeys } from '@/api/admin/users'
import { getBatchUserAttributes, listEnabledDefinitions } from '@/api/admin/userAttributes'

describe('user list secondary request cancellation', () => {
  beforeEach(() => {
    get.mockReset().mockResolvedValue({ data: {} })
    post.mockReset().mockResolvedValue({ data: {} })
  })

  it('forwards a shared cancellation signal to each HTTP request', async () => {
    const signal = new AbortController().signal
    await getBatchUsersUsage([42], { signal })
    await getBatchUserAttributes([42], { signal })
    await getPlatformQuotas(42, { signal })
    await getUserApiKeys(42, { signal })
    await listEnabledDefinitions({ signal })
    expect(post).toHaveBeenCalledWith('/admin/dashboard/users-usage', { user_ids: [42] }, { signal })
    expect(post).toHaveBeenCalledWith('/admin/user-attributes/batch', { user_ids: [42] }, { signal })
    expect(get).toHaveBeenCalledWith('/admin/users/42/platform-quotas', { signal })
    expect(get).toHaveBeenCalledWith('/admin/users/42/api-keys', { signal })
    expect(get).toHaveBeenCalledWith('/admin/user-attributes', { params: { enabled: true }, signal })
  })

  it('continues to support callers without request options', async () => {
    await expect(getBatchUsersUsage([42])).resolves.toEqual({})
    await expect(getBatchUserAttributes([42])).resolves.toEqual({})
    await expect(getPlatformQuotas(42)).resolves.toEqual({})
    await expect(getUserApiKeys(42)).resolves.toEqual({})
    await expect(listEnabledDefinitions()).resolves.toEqual({})
  })
})
