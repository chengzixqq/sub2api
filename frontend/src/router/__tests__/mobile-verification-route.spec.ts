import type { RouteRecordRaw } from 'vue-router'
import { describe, expect, it, vi } from 'vitest'

type Guard = (to: { name: string; path: string }, from: object, next: ReturnType<typeof vi.fn>) => Promise<void>
const test = vi.hoisted(() => ({
  routes: [] as RouteRecordRaw[], guard: undefined as Guard | undefined,
  auth: vi.fn(() => { throw new Error('Auth store must not be accessed') }),
  app: vi.fn(() => { throw new Error('App settings store must not be accessed') })
}))

vi.mock('vue-router', () => ({
  createWebHistory: () => ({}),
  createRouter: (options: { routes: RouteRecordRaw[] }) => {
    test.routes = options.routes
    return {
      beforeEach: (guard: Guard) => { test.guard = guard },
      afterEach: vi.fn(), onError: vi.fn()
    }
  }
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: test.auth }))
vi.mock('@/stores/app', () => ({ useAppStore: test.app }))
vi.mock('@/stores/adminSettings', () => ({ useAdminSettingsStore: vi.fn() }))
vi.mock('@/stores/adminCompliance', () => ({ useAdminComplianceStore: vi.fn() }))
vi.mock('@/composables/useNavigationLoading', () => ({
  useNavigationLoadingState: () => ({ startNavigation: vi.fn(), endNavigation: vi.fn() })
}))
vi.mock('@/composables/useRoutePrefetch', () => ({ useRoutePrefetch: vi.fn() }))

describe('public mobile verification route', () => {
  it('allows the exact challenge route before authentication and backend-mode processing', async () => {
    await import('@/router')
    const route = test.routes.find((item) => item.path === '/mobile/verify')
    expect(route?.name).toBe('MobileVerify')
    expect(route?.meta?.requiresAuth).toBe(false)
    const next = vi.fn()
    await test.guard!({ name: 'MobileVerify', path: '/mobile/verify' }, {}, next)
    expect(next).toHaveBeenCalledOnce()
    expect(next).toHaveBeenCalledWith()
    expect(test.auth).not.toHaveBeenCalled()
    expect(test.app).not.toHaveBeenCalled()
  })
})
