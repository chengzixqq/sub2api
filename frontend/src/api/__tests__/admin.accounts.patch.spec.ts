import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, patch } = vi.hoisted(() => ({
  get: vi.fn(),
  patch: vi.fn()
}))

vi.mock('@/api/client', () => ({
  apiClient: { get, patch }
}))

import { getByIdWithEtag, patchAccount } from '@/api/admin/accounts'

describe('admin account versioned edit API', () => {
  beforeEach(() => {
    get.mockReset()
    patch.mockReset()
  })

  it('returns the ETag read with the full account detail', async () => {
    const account = { id: 27, config_version: 6 }
    get.mockResolvedValueOnce({ data: account, headers: { etag: '"cv-6"' } })

    await expect(getByIdWithEtag(27)).resolves.toEqual({ account, etag: '"cv-6"' })
    expect(get).toHaveBeenCalledWith('/admin/accounts/27')
  })

  it('uses that ETag for the single sparse edit request', async () => {
    const payload = { name: 'updated', dispatch_scope: 'authorized_only' }
    const account = { id: 27, name: 'updated' }
    patch.mockResolvedValueOnce({ data: account })

    await expect(patchAccount(27, payload, '"cv-6"')).resolves.toEqual(account)
    expect(patch).toHaveBeenCalledTimes(1)
    expect(patch).toHaveBeenCalledWith('/admin/accounts/27', payload, {
      headers: { 'If-Match': '"cv-6"' }
    })
  })

  it('does not send an edit without its original ETag', async () => {
    await expect(patchAccount(27, { name: 'updated' }, '')).rejects.toThrow('Account ETag is missing')
    expect(patch).not.toHaveBeenCalled()
  })
})
