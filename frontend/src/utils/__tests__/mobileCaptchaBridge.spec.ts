import { describe, expect, it, vi } from 'vitest'
import {
  createMobileCaptchaBridge,
  resolveMobileCaptchaProvider,
  type MobileBridgeEnvironment,
  type MobileCaptchaProvider,
  type MobileCaptchaSettings
} from '../mobileCaptchaBridge'

const nonce = '3a006827-701c-44e8-b153-cef42dc70234'
function environment(overrides: Partial<MobileBridgeEnvironment> = {}): MobileBridgeEnvironment {
  return {
    url: `https://station.example/mobile/verify?nonce=${nonce}`,
    secureContext: true, mainFrame: true,
    handler: { postMessage: vi.fn() }, ...overrides
  }
}

describe('native captcha bridge', () => {
  it.each([
    ['turnstile', 'turnstile-proof', '', { turnstile_token: 'turnstile-proof' }],
    ['aliyun', 'aliyun-verify-param', '', { turnstile_token: 'aliyun-verify-param' }],
    ['tencent', 'ticket', 'randstr', { tencent_captcha_ticket: 'ticket', tencent_captcha_randstr: 'randstr' }]
  ] as const)('emits only the %s login proof fields', (provider, token, randstr, proof) => {
    const context = environment()
    const bridge = createMobileCaptchaBridge(() => context)
    expect(bridge.sendProof(provider, token, randstr)).toBe(true)
    expect(context.handler?.postMessage).toHaveBeenCalledOnce()
    expect(context.handler?.postMessage).toHaveBeenCalledWith({ version: 1, nonce, provider, proof })
    expect(bridge.sendProof(provider, token, randstr)).toBe(false)
    expect(bridge.cancel()).toBe(false)
  })

  it.each([
    { secureContext: false }, { mainFrame: false }, { handler: undefined },
    { url: `http://station.example/mobile/verify?nonce=${nonce}` },
    { url: 'https://station.example/mobile/verify?nonce=invalid' },
    { url: `https://station.example/mobile/verify?nonce=${nonce}&nonce=${nonce}` },
    { url: `https://station.example/mobile/verify?nonce=${nonce}&password=secret` },
    { url: `https://secret@station.example/mobile/verify?nonce=${nonce}` },
    { url: `https://station.example/mobile/verify?nonce=${nonce}#token` },
    { url: `https://station.example/login?nonce=${nonce}` }
  ])('rejects invalid nonce or untrusted context %j without posting', (override) => {
    const context = environment(override)
    expect(() => createMobileCaptchaBridge(() => context)).toThrow()
    if (context.handler) expect(context.handler.postMessage).not.toHaveBeenCalled()
  })

  it('rechecks nonce and origin immediately before posting', () => {
    let current = environment()
    const bridge = createMobileCaptchaBridge(() => current)
    current = { ...current, url: `https://other.example/mobile/verify?nonce=${nonce}` }
    expect(() => bridge.sendProof('turnstile', 'proof')).toThrow()
    expect(current.handler?.postMessage).not.toHaveBeenCalled()
  })

  it('sends cancellation without a proof, provider, or secrets and completes once', () => {
    const context = environment()
    const bridge = createMobileCaptchaBridge(() => context)
    expect(bridge.cancel()).toBe(true)
    expect(context.handler?.postMessage).toHaveBeenCalledOnce()
    expect(context.handler?.postMessage).toHaveBeenCalledWith({ version: 1, nonce, cancelled: true })
    expect(bridge.sendProof('turnstile', 'old-proof')).toBe(false)
  })

  it.each([
    ['turnstile', '', ''], ['aliyun', ' ', ''], ['tencent', 'ticket', ''],
    ['unknown' as MobileCaptchaProvider, 'proof', '']
  ])('rejects incomplete and unknown provider proof %s', (provider, token, randstr) => {
    const context = environment()
    const bridge = createMobileCaptchaBridge(() => context)
    expect(() => bridge.sendProof(provider as MobileCaptchaProvider, token, randstr)).toThrow()
    expect(context.handler?.postMessage).not.toHaveBeenCalled()
  })

  it('does not read credentials or include SDK fields in messages', () => {
    localStorage.setItem('auth_token', 'private-access-token')
    localStorage.setItem('refresh_token', 'private-refresh-token')
    const context = environment()
    const bridge = createMobileCaptchaBridge(() => context)
    bridge.sendProof('turnstile', 'captcha-proof', 'ignored-extra')
    expect(context.handler?.postMessage).toHaveBeenCalledWith({
      version: 1, nonce, provider: 'turnstile', proof: { turnstile_token: 'captcha-proof' }
    })
    localStorage.clear()
  })
})

describe('native captcha provider selection', () => {
  const disabled: MobileCaptchaSettings = { turnstile_enabled: false, turnstile_site_key: '' }
  it('reports disabled captcha without inventing a proof', () => {
    expect(resolveMobileCaptchaProvider(disabled)).toBeNull()
  })
  it('preserves server provider precedence', () => {
    expect(resolveMobileCaptchaProvider({ ...disabled, turnstile_enabled: true, turnstile_site_key: 'site', tencent_captcha_enabled: true, tencent_captcha_app_id: 'app' })).toBe('turnstile')
    expect(resolveMobileCaptchaProvider({ ...disabled, tencent_captcha_enabled: true, tencent_captcha_app_id: 'app', aliyun_captcha_enabled: true, aliyun_captcha_scene_id: 'scene', aliyun_captcha_prefix: 'prefix' })).toBe('tencent')
    expect(resolveMobileCaptchaProvider({ ...disabled, aliyun_captcha_enabled: true, aliyun_captcha_scene_id: 'scene', aliyun_captcha_prefix: 'prefix' })).toBe('aliyun')
  })
  it.each([
    { turnstile_enabled: true }, { tencent_captcha_enabled: true },
    { aliyun_captcha_enabled: true, aliyun_captcha_scene_id: 'scene' }
  ])('fails closed for enabled but incomplete provider %j', (config) => {
    expect(() => resolveMobileCaptchaProvider({ ...disabled, ...config })).toThrow()
  })
})
