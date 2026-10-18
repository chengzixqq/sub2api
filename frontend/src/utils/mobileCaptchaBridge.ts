import type { PublicSettings } from '@/types'

export type MobileCaptchaProvider = 'turnstile' | 'tencent' | 'aliyun'
export type MobileCaptchaSettings = Pick<PublicSettings,
  | 'turnstile_enabled' | 'turnstile_site_key'
  | 'tencent_captcha_enabled' | 'tencent_captcha_app_id' | 'tencent_captcha_region'
  | 'aliyun_captcha_enabled' | 'aliyun_captcha_scene_id' | 'aliyun_captcha_prefix'
  | 'aliyun_captcha_region'>

interface CaptchaProof {
  turnstile_token?: string
  tencent_captcha_ticket?: string
  tencent_captcha_randstr?: string
}

export type MobileCaptchaEnvelope =
  | { version: 1; nonce: string; provider: MobileCaptchaProvider; proof: CaptchaProof }
  | { version: 1; nonce: string; cancelled: true }

export interface MobileBridgeEnvironment {
  url: string
  secureContext: boolean
  mainFrame: boolean
  handler?: { postMessage: (message: MobileCaptchaEnvelope) => void }
}

const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

function validateContext(environment: MobileBridgeEnvironment): string {
  const url = new URL(environment.url)
  if (url.protocol !== 'https:' || !environment.secureContext || !environment.mainFrame) {
    throw new Error('请从 Sub2 Mobile 的安全验证窗口打开此页面。')
  }
  if (url.pathname !== '/mobile/verify' || url.username || url.password || url.hash) {
    throw new Error('验证地址无效，请返回 App 重新开始。')
  }
  const nonce = url.searchParams.get('nonce') ?? ''
  if (!UUID_PATTERN.test(nonce) || url.searchParams.getAll('nonce').length !== 1
    || Array.from(url.searchParams.keys()).some((key) => key !== 'nonce')) {
    throw new Error('验证请求标识无效，请返回 App 重新开始。')
  }
  if (typeof environment.handler?.postMessage !== 'function') {
    throw new Error('未找到手机端验证通道，请在 Sub2 Mobile 内重试。')
  }
  return nonce
}

// Match the server and CaptchaChallenge priority; an enabled but incomplete provider fails closed.
export function resolveMobileCaptchaProvider(settings: MobileCaptchaSettings): MobileCaptchaProvider | null {
  if (settings.turnstile_enabled) {
    if (!settings.turnstile_site_key?.trim()) throw new Error('站点验证码配置不完整，请联系管理员。')
    return 'turnstile'
  }
  if (settings.tencent_captcha_enabled) {
    if (!settings.tencent_captcha_app_id?.trim()) throw new Error('站点验证码配置不完整，请联系管理员。')
    return 'tencent'
  }
  if (settings.aliyun_captcha_enabled) {
    if (!settings.aliyun_captcha_scene_id?.trim() || !settings.aliyun_captcha_prefix?.trim()) {
      throw new Error('站点验证码配置不完整，请联系管理员。')
    }
    return 'aliyun'
  }
  return null
}

// Construct a new allowlisted message instead of forwarding an SDK or settings object.
export function createMobileCaptchaBridge(readEnvironment: () => MobileBridgeEnvironment) {
  const initial = readEnvironment()
  const nonce = validateContext(initial)
  const origin = new URL(initial.url).origin
  let completed = false

  function post(message: MobileCaptchaEnvelope): boolean {
    if (completed) return false
    const current = readEnvironment()
    if (validateContext(current) !== nonce || new URL(current.url).origin !== origin) {
      throw new Error('验证上下文已改变，请返回 App 重新开始。')
    }
    current.handler!.postMessage(message)
    completed = true
    return true
  }

  return {
    sendProof(provider: MobileCaptchaProvider, token: string, randstr = ''): boolean {
      if (!['turnstile', 'tencent', 'aliyun'].includes(provider)
        || typeof token !== 'string' || !token.trim()
        || (provider === 'tencent' && (typeof randstr !== 'string' || !randstr.trim()))) {
        throw new Error('验证码结果无效，请重新验证。')
      }
      const proof: CaptchaProof = provider === 'tencent'
        ? { tencent_captcha_ticket: token, tencent_captcha_randstr: randstr }
        : { turnstile_token: token }
      return post({ version: 1, nonce, provider, proof })
    },
    cancel(): boolean {
      return post({ version: 1, nonce, cancelled: true })
    }
  }
}

export function readMobileBridgeEnvironment(): MobileBridgeEnvironment {
  const bridgeWindow = window as Window & {
    webkit?: { messageHandlers?: { sub2Captcha?: MobileBridgeEnvironment['handler'] } }
  }
  return {
    url: window.location.href,
    secureContext: window.isSecureContext,
    mainFrame: window.top === window.self,
    handler: bridgeWindow.webkit?.messageHandlers?.sub2Captcha
  }
}
