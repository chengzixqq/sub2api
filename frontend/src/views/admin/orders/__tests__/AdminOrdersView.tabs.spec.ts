import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'

const { route, router, adminSettingsStore, getOrders } = vi.hoisted(() => ({
  route: { query: {} as Record<string, string> },
  router: { push: vi.fn(), replace: vi.fn() },
  adminSettingsStore: {
    loaded: true,
    paymentEnabled: true,
    fetch: vi.fn()
  },
  getOrders: vi.fn()
}))

vi.mock('vue-router', () => ({
  useRoute: () => route,
  useRouter: () => router
}))

vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key })
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() })
}))

vi.mock('@/stores/adminSettings', () => ({
  useAdminSettingsStore: () => adminSettingsStore
}))

vi.mock('@/api/admin/payment', () => {
  const api = {
    getOrders,
    getOrder: vi.fn(),
    cancelOrder: vi.fn(),
    retryRecharge: vi.fn(),
    refundOrder: vi.fn(),
    queryRefund: vi.fn()
  }
  return { adminPaymentAPI: api, default: api }
})

import AdminOrdersView from '../AdminOrdersView.vue'

const globalStubs = {
  AppLayout: { template: '<div><slot /></div>' },
  UserAdjustmentsPanel: { template: '<div data-test="manual-adjustments-panel" />' },
  OrderTable: { template: '<div data-test="payment-orders-panel" />' },
  Pagination: true,
  BaseDialog: true,
  Select: true,
  Icon: true,
  AdminRefundDialog: true,
  OrderStatusBadge: true
}

function mountView() {
  return shallowMount(AdminOrdersView, { global: { stubs: globalStubs } })
}

describe('AdminOrdersView tabs', () => {
  beforeEach(() => {
    route.query = {}
    router.push.mockReset()
    router.replace.mockReset()
    adminSettingsStore.loaded = true
    adminSettingsStore.paymentEnabled = true
    adminSettingsStore.fetch.mockReset().mockResolvedValue(undefined)
    getOrders.mockReset().mockResolvedValue({ data: { items: [], total: 0 } })
  })

  it('defaults to payment orders when payment is enabled and can deep-link to manual adjustments', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(getOrders).toHaveBeenCalledOnce()
    expect(wrapper.get('[data-test="payment-orders-panel"]').isVisible()).toBe(true)
    expect(wrapper.find('[data-test="manual-adjustments-panel"]').exists()).toBe(false)

    const manualTab = wrapper.findAll('[role="tab"]').find((tab) =>
      tab.text().includes('payment.admin.tabs.userAdjustments')
    )
    expect(manualTab).toBeDefined()
    await manualTab!.trigger('click')

    expect(wrapper.get('[data-test="manual-adjustments-panel"]').isVisible()).toBe(true)
    expect(router.push).toHaveBeenCalledWith({ query: { tab: 'manual' } })
    wrapper.unmount()
  })

  it('honors ?tab=manual without loading payment orders', async () => {
    route.query = { tab: 'manual' }
    const wrapper = mountView()
    await flushPromises()

    expect(getOrders).not.toHaveBeenCalled()
    expect(wrapper.get('[data-test="manual-adjustments-panel"]').isVisible()).toBe(true)
    wrapper.unmount()
  })

  it('keeps manual adjustments reachable and hides payment orders when payment is disabled', async () => {
    adminSettingsStore.paymentEnabled = false
    const wrapper = mountView()
    await flushPromises()

    expect(getOrders).not.toHaveBeenCalled()
    expect(wrapper.findAll('[role="tab"]')).toHaveLength(1)
    expect(wrapper.get('[role="tab"]').text()).toContain('payment.admin.tabs.userAdjustments')
    expect(wrapper.get('[data-test="manual-adjustments-panel"]').isVisible()).toBe(true)
    expect(router.replace).toHaveBeenCalledWith({ query: { tab: 'manual' } })
    wrapper.unmount()
  })
})
