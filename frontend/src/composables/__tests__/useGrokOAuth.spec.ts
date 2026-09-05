import { describe, expect, it, vi } from 'vitest'

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn()
  })
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => {
      const messages: Record<string, string> = {
        'admin.accounts.oauth.grok.failedToExchangeCode': 'Grok 授权码兑换失败',
        'admin.accounts.oauth.grok.errors.GROK_OAUTH_INVALID_STATE':
          'Grok OAuth state 与当前会话不匹配。请粘贴同一次生成的授权链接返回的回调 URL。'
      }
      return messages[key] ?? key
    }
  })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    grok: {
      generateAuthUrl: vi.fn(),
      exchangeCode: vi.fn(),
      refreshGrokToken: vi.fn(),
      validateSSOToken: vi.fn()
    }
  }
}))

import { useGrokOAuth } from '@/composables/useGrokOAuth'
import { adminAPI } from '@/api/admin'

describe('useGrokOAuth.exchangeAuthCode', () => {
  it('shows a state mismatch recovery hint from structured backend errors', async () => {
    vi.mocked(adminAPI.grok.exchangeCode).mockRejectedValueOnce({
      status: 400,
      reason: 'GROK_OAUTH_INVALID_STATE',
      message: 'invalid oauth state'
    })
    const oauth = useGrokOAuth()

    const tokenInfo = await oauth.exchangeAuthCode({
      code: 'code',
      sessionId: 'session-id',
      state: 'wrong-state'
    })

    expect(tokenInfo).toBeNull()
    expect(oauth.error.value).toBe(
      'Grok OAuth state 与当前会话不匹配。请粘贴同一次生成的授权链接返回的回调 URL。'
    )
  })
})

describe('useGrokOAuth account scoping', () => {
  it('forwards the existing account id to re-auth helpers', async () => {
    vi.mocked(adminAPI.grok.generateAuthUrl).mockResolvedValueOnce({
      auth_url: 'https://example.test/auth',
      session_id: 'session',
      state: 'state'
    })
    vi.mocked(adminAPI.grok.exchangeCode).mockResolvedValueOnce({ access_token: 'access' })
    vi.mocked(adminAPI.grok.refreshGrokToken).mockResolvedValueOnce({ access_token: 'refresh' })
    vi.mocked(adminAPI.grok.validateSSOToken).mockResolvedValueOnce({ access_token: 'sso' })

    const oauth = useGrokOAuth()
    await oauth.generateAuthUrl(7, 42)
    await oauth.exchangeAuthCode({
      code: 'code',
      sessionId: 'session',
      state: 'state',
      proxyId: 7,
      accountId: 42
    })
    await oauth.validateRefreshToken('refresh-token', 7, 42)
    await oauth.validateSSOToken('sso-token', 7, 42)

    expect(adminAPI.grok.generateAuthUrl).toHaveBeenCalledWith({ proxy_id: 7, account_id: 42 })
    expect(adminAPI.grok.exchangeCode).toHaveBeenCalledWith({
      session_id: 'session',
      state: 'state',
      code: 'code',
      proxy_id: 7,
      account_id: 42
    })
    expect(adminAPI.grok.refreshGrokToken).toHaveBeenCalledWith('refresh-token', 7, 42)
    expect(adminAPI.grok.validateSSOToken).toHaveBeenCalledWith('sso-token', 7, 42)
  })
})

describe('useGrokOAuth.buildCredentials', () => {
  it('builds OAuth credentials without forcing base_url or leaking sso/password', () => {
    const oauth = useGrokOAuth()

    const credentials = oauth.buildCredentials({
      access_token: 'access-token',
      token_type: 'Bearer',
      expires_at: 1_900_000_000,
      client_id: 'client-id',
      scope: 'openid grok-cli:access',
      email: 'grok@example.com',
      password: 'super-secret',
      sso_token: 'sso-cookie',
      sso: 'sso-cookie',
      'sso-rw': 'sso-cookie'
    } as any)

    expect(credentials.access_token).toBe('access-token')
    expect(credentials.email).toBe('grok@example.com')
    // System/CLI mode chooses the correct host; do not pin public API URL.
    expect(credentials.base_url).toBeUndefined()
    expect(credentials).not.toHaveProperty('password')
    expect(credentials).not.toHaveProperty('sso_token')
    expect(credentials).not.toHaveProperty('sso')
    expect(credentials).not.toHaveProperty('sso-rw')
  })
})
