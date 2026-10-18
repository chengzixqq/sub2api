import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises } from '@vue/test-utils'

const test = vi.hoisted(() => {
  const mount = vi.fn()
  const use = vi.fn()
  const app = { use, mount }
  use.mockReturnValue(app)
  return {
    app, createApp: vi.fn(() => app), createPinia: vi.fn(),
    useAppStore: vi.fn(), isReady: vi.fn(), initI18n: vi.fn().mockResolvedValue(undefined),
    view: { name: 'MobileVerifyView' }
  }
})

vi.mock('vue', async (original) => ({ ...await original<typeof import('vue')>(), createApp: test.createApp }))
vi.mock('pinia', () => ({ createPinia: test.createPinia }))
vi.mock('@/App.vue', () => ({ default: { name: 'WebApp' } }))
vi.mock('@/router', () => ({ default: { isReady: test.isReady } }))
vi.mock('@/i18n', () => ({ default: { install: vi.fn() }, initI18n: test.initI18n }))
vi.mock('@/stores/app', () => ({ useAppStore: test.useAppStore }))
vi.mock('@/utils/branding', () => ({ updateFavicon: vi.fn() }))
vi.mock('@/utils/device', () => ({ isIOSDevice: () => false }))
vi.mock('@/views/public/MobileVerifyView.vue', () => ({ default: test.view }))

describe('direct native verification bootstrap', () => {
  afterEach(() => { window.history.replaceState({}, '', '/') })

  it('mounts only the challenge without Pinia, Web auth/settings stores, or Web router', async () => {
    window.history.replaceState({}, '', '/mobile/verify?nonce=3a006827-701c-44e8-b153-cef42dc70234')
    await import('@/main')
    await flushPromises()
    expect(test.createApp).toHaveBeenCalledWith(test.view)
    expect(test.app.mount).toHaveBeenCalledWith('#app')
    expect(test.createPinia).not.toHaveBeenCalled()
    expect(test.useAppStore).not.toHaveBeenCalled()
    expect(test.isReady).not.toHaveBeenCalled()
  })
})
