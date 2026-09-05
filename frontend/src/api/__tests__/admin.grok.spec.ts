import { beforeEach, describe, expect, it, vi } from 'vitest'

const { post, get } = vi.hoisted(() => ({
  post: vi.fn(),
  get: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: { post, get },
}))

import {
  authorizePassword,
  createFromSSO,
  exchangeCode,
  generateAuthUrl,
  getCapabilities,
  getGrokSSOImportTimeout,
  refreshGrokToken,
  validateSSOToken,
} from '@/api/admin/grok'

describe('admin Grok SSO import API', () => {
  beforeEach(() => {
    post.mockReset()
    get.mockReset()
    post.mockResolvedValue({ data: { created: [], failed: [] } })
    get.mockResolvedValue({ data: { password_auth_enabled: false } })
  })

  it.each([
    [1, 180_000],
    [3, 180_000],
    [4, 270_000],
    [7, 360_000],
  ])('uses a timeout sized for %i keys', async (keyCount, expectedTimeout) => {
    expect(getGrokSSOImportTimeout(keyCount)).toBe(expectedTimeout)

    await createFromSSO({
      sso_tokens: Array.from({ length: keyCount }, (_, index) => `sso-${index + 1}`),
    })

    expect(post).toHaveBeenCalledWith(
      '/admin/grok/sso-to-oauth',
      expect.objectContaining({ sso_tokens: expect.any(Array) }),
      { timeout: expectedTimeout },
    )
  })

  it('preserves password whitespace and applies the authorization timeout', async () => {
    post.mockResolvedValueOnce({ data: { access_token: 'access-token' } })

    await authorizePassword(' user@example.com ----  password with spaces  ', 7)

    expect(post).toHaveBeenCalledWith(
      '/admin/grok/oauth/password',
      {
        email: 'user@example.com',
        password: '  password with spaces  ',
        proxy_id: 7,
      },
      { timeout: 120_000 },
    )
  })

  it('carries account scope through re-auth helpers', async () => {
    post.mockResolvedValue({ data: { access_token: 'access-token' } })

    await generateAuthUrl({ account_id: 42, proxy_id: 7 })
    await exchangeCode({ account_id: 42, session_id: 'session', state: 'state', code: 'code' })
    await refreshGrokToken('refresh-token', 7, 42)
    await validateSSOToken('sso-token', 7, 42)
    await getCapabilities(42)

    expect(post).toHaveBeenNthCalledWith(1, '/admin/grok/oauth/auth-url', {
      account_id: 42,
      proxy_id: 7,
    })
    expect(post).toHaveBeenNthCalledWith(2, '/admin/grok/oauth/exchange-code', {
      account_id: 42,
      session_id: 'session',
      state: 'state',
      code: 'code',
    })
    expect(post).toHaveBeenNthCalledWith(
      3,
      '/admin/grok/oauth/refresh-token',
      { refresh_token: 'refresh-token', proxy_id: 7, account_id: 42 },
    )
    expect(post).toHaveBeenNthCalledWith(
      4,
      '/admin/grok/oauth/sso-token',
      { sso_token: 'sso-token', proxy_id: 7, account_id: 42 },
      { timeout: 120_000 },
    )
    expect(get).toHaveBeenCalledWith('/admin/grok/oauth/capabilities', {
      params: { account_id: 42 },
    })
  })
})
