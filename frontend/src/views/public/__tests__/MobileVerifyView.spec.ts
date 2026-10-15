import { defineComponent, h } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import MobileVerifyView from '../MobileVerifyView.vue'
import type { MobileCaptchaSettings } from '@/utils/mobileCaptchaBridge'

const test = vi.hoisted(() => ({
  postMessage: vi.fn(), verifyAction: vi.fn(), secureContext: true, mainFrame: true,
  url: 'https://station.example/mobile/verify?nonce=3a006827-701c-44e8-b153-cef42dc70234'
}))

vi.mock('@/utils/mobileCaptchaBridge', async (original) => ({
  ...await original<typeof import('@/utils/mobileCaptchaBridge')>(),
  readMobileBridgeEnvironment: () => ({
    url: test.url, secureContext: test.secureContext, mainFrame: test.mainFrame,
    handler: { postMessage: test.postMessage }
  })
}))

const CaptchaStub = defineComponent({
  name: 'CaptchaChallenge', emits: ['verify', 'expire', 'error'],
  setup(_, { expose }) {
    expose({ verifyAction: test.verifyAction })
    return () => h('div', { 'data-testid': 'challenge' })
  }
})

let wrapper: VueWrapper | undefined
let fetchMock: ReturnType<typeof vi.fn>
const nonce = '3a006827-701c-44e8-b153-cef42dc70234'
const turnstileSettings = { turnstile_enabled: true, turnstile_site_key: 'public-site-key' }

async function openPage(settings: MobileCaptchaSettings = turnstileSettings) {
  fetchMock.mockResolvedValue({ ok: true, json: async () => ({ code: 0, data: settings }) })
  wrapper = mount(MobileVerifyView, { global: { stubs: { CaptchaChallenge: CaptchaStub } } })
  await flushPromises()
  return wrapper
}

async function startChallenge() {
  const button = wrapper!.findAll('button').find((item) => item.text().includes('开始验证'))!
  await button.trigger('click')
  await flushPromises()
}

describe('MobileVerifyView', () => {
  beforeEach(() => {
    test.postMessage.mockReset()
    test.verifyAction.mockReset()
    test.secureContext = true
    test.mainFrame = true
    test.url = `https://station.example/mobile/verify?nonce=${nonce}`
    fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
  })
  afterEach(() => {
    wrapper?.unmount()
    wrapper = undefined
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  it('uses an unauthenticated same-origin settings request and waits for user action', async () => {
    await openPage()
    expect(fetchMock).toHaveBeenCalledWith('/api/v1/settings/public', expect.objectContaining({ credentials: 'omit', cache: 'no-store', headers: { Accept: 'application/json' } }))
    expect(wrapper!.find('[data-testid="challenge"]').exists()).toBe(false)
    expect(test.postMessage).not.toHaveBeenCalled()
    await startChallenge()
    wrapper!.findComponent(CaptchaStub).vm.$emit('verify', 'captcha-proof', '')
    await flushPromises()
    expect(test.postMessage).toHaveBeenCalledOnce()
    expect(test.postMessage).toHaveBeenCalledWith({ version: 1, nonce, provider: 'turnstile', proof: { turnstile_token: 'captcha-proof' } })
    expect(wrapper!.text()).toContain('验证结果已提交')
  })

  it.each([
    ['tencent', { turnstile_enabled: false, turnstile_site_key: '', tencent_captcha_enabled: true, tencent_captcha_app_id: 'app-id' }, { token: 'ticket', randstr: 'random' }, { tencent_captcha_ticket: 'ticket', tencent_captcha_randstr: 'random' }],
    ['aliyun', { turnstile_enabled: false, turnstile_site_key: '', aliyun_captcha_enabled: true, aliyun_captcha_scene_id: 'scene', aliyun_captcha_prefix: 'prefix' }, { token: 'verify-param', randstr: '' }, { turnstile_token: 'verify-param' }]
  ] as const)('returns the %s action proof through the native bridge', async (provider, settings, action, proof) => {
    test.verifyAction.mockResolvedValue(action)
    await openPage(settings)
    await startChallenge()
    expect(test.postMessage).toHaveBeenCalledOnce()
    expect(test.postMessage).toHaveBeenCalledWith({ version: 1, nonce, provider, proof })
  })

  it.each(['expire', 'error'])('discards %s challenge state and allows a fresh retry', async (event) => {
    await openPage()
    await startChallenge()
    wrapper!.findComponent(CaptchaStub).vm.$emit(event)
    await flushPromises()
    expect(wrapper!.find('[role="alert"]').exists()).toBe(true)
    expect(wrapper!.find('[data-testid="challenge"]').exists()).toBe(false)
    expect(test.postMessage).not.toHaveBeenCalled()
    await startChallenge()
    expect(wrapper!.find('[data-testid="challenge"]').exists()).toBe(true)
  })

  it('times out a challenge that never returns and permits a fresh one', async () => {
    await openPage()
    vi.useFakeTimers()
    await startChallenge()
    await vi.advanceTimersByTimeAsync(120_000)
    expect(wrapper!.text()).toContain('验证等待超时')
    expect(wrapper!.find('[data-testid="challenge"]').exists()).toBe(false)
    expect(test.postMessage).not.toHaveBeenCalled()
    await startChallenge()
    expect(wrapper!.find('[data-testid="challenge"]').exists()).toBe(true)
  })

  it('does not forward an action result that arrives after cancellation', async () => {
    let resolve!: (value: { token: string; randstr: string }) => void
    test.verifyAction.mockReturnValue(new Promise((done) => { resolve = done }))
    await openPage({ turnstile_enabled: false, turnstile_site_key: '', tencent_captcha_enabled: true, tencent_captcha_app_id: 'app-id' })
    await startChallenge()
    await wrapper!.findAll('button').find((button) => button.text() === '取消并返回')!.trigger('click')
    resolve({ token: 'late-ticket', randstr: 'late-random' })
    await flushPromises()
    expect(test.postMessage).toHaveBeenCalledOnce()
    expect(test.postMessage).toHaveBeenCalledWith({ version: 1, nonce, cancelled: true })
  })

  it('reports provider-disabled and network failures without emitting fake proofs', async () => {
    await openPage({ turnstile_enabled: false, turnstile_site_key: '' })
    expect(wrapper!.text()).toContain('站点未启用验证码')
    expect(test.postMessage).not.toHaveBeenCalled()
    wrapper!.unmount()
    fetchMock.mockRejectedValue(new Error('private-server-error'))
    wrapper = mount(MobileVerifyView, { global: { stubs: { CaptchaChallenge: CaptchaStub } } })
    await flushPromises()
    expect(wrapper.text()).toContain('无法读取站点验证码设置')
    expect(wrapper.text()).not.toContain('private-server-error')
    expect(test.postMessage).not.toHaveBeenCalled()
  })

  it.each(['nonce', 'context'])('rejects invalid %s before fetching settings or rendering providers', async (invalid) => {
    if (invalid === 'nonce') test.url = 'https://station.example/mobile/verify?nonce=invalid'
    else test.mainFrame = false
    await openPage()
    expect(fetchMock).not.toHaveBeenCalled()
    expect(wrapper!.find('[role="alert"]').exists()).toBe(true)
    expect(test.postMessage).not.toHaveBeenCalled()
  })
})
