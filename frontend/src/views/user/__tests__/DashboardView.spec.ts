import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import DashboardView from '../DashboardView.vue'

const { refreshUser, getStats, getTrend, getModels, getRecent, getQuotas } = vi.hoisted(() => ({
  refreshUser: vi.fn(), getStats: vi.fn(), getTrend: vi.fn(), getModels: vi.fn(),
  getRecent: vi.fn(), getQuotas: vi.fn()
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ user: { balance: 10 }, isSimpleMode: false, refreshUser })
}))
vi.mock('@/api/usage', () => ({ usageAPI: {
  getDashboardStats: getStats, getDashboardTrend: getTrend,
  getDashboardModels: getModels, getByDateRange: getRecent
} }))
vi.mock('@/api/user', () => ({ getMyPlatformQuotas: getQuotas }))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key })
}))

const mountView = () => mount(DashboardView, { global: { stubs: {
  AppLayout: { template: '<div><slot /></div>' }, LoadingSpinner: true,
  UserDashboardStats: { name: 'UserDashboardStats', props: ['stats'], template: '<div data-test="stats">{{ stats.total_requests }}</div>' },
  UserDashboardCharts: { name: 'UserDashboardCharts', props: ['trend', 'models', 'loading'], emits: ['refresh', 'dateRangeChange'], template: '<div data-test="charts" />' },
  UserDashboardRecentUsage: { name: 'UserDashboardRecentUsage', props: ['data', 'loading'], template: '<div data-test="recent" />' },
  UserDashboardQuickActions: { template: '<div data-test="actions" />' }
} } })

describe('user dashboard independent loading', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    refreshUser.mockResolvedValue({})
    getStats.mockResolvedValue({ total_requests: 42 })
    getTrend.mockResolvedValue({ trend: [] })
    getModels.mockResolvedValue({ models: [] })
    getRecent.mockResolvedValue({ items: [] })
    getQuotas.mockResolvedValue({ platform_quotas: [] })
  })

  it('starts statistics before auth refresh finishes and keeps the layout visible while they are pending', async () => {
    refreshUser.mockReturnValueOnce(new Promise(() => {}))
    getStats.mockReturnValueOnce(new Promise(() => {}))
    const wrapper = mountView()
    await flushPromises()
    expect(getStats).toHaveBeenCalledOnce()
    expect(wrapper.find('[data-test="charts"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="recent"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="actions"]').exists()).toBe(true)
    expect(getRecent).toHaveBeenCalledWith(expect.any(String), expect.any(String), undefined,
      expect.objectContaining({ pageSize: 5, countMode: 'deferred', signal: expect.any(AbortSignal) }))
    wrapper.unmount()
    expect(getStats.mock.calls[0][0].signal.aborted).toBe(true)
  })

  it('ignores stale chart results after a date change and cancels all optional requests on unmount', async () => {
    let resolveOld!: (value: unknown) => void
    getTrend.mockReturnValueOnce(new Promise((resolve) => { resolveOld = resolve }))
    const wrapper = mountView()
    await flushPromises()
    const oldSignal = getTrend.mock.calls[0][1].signal as AbortSignal
    getTrend.mockResolvedValueOnce({ trend: [{ date: 'new' }] })
    wrapper.findComponent({ name: 'UserDashboardCharts' }).vm.$emit('dateRangeChange')
    await flushPromises()
    expect(oldSignal.aborted).toBe(true)
    resolveOld({ trend: [{ date: 'old' }] })
    await flushPromises()
    expect(wrapper.findComponent({ name: 'UserDashboardCharts' }).props('trend')).toEqual([{ date: 'new' }])
    expect(wrapper.get('[data-test="stats"]').text()).toBe('42')
    wrapper.unmount()
    expect(getTrend.mock.calls[1][1].signal.aborted).toBe(true)
    expect(getRecent.mock.calls[0][3].signal.aborted).toBe(true)
    expect(getQuotas.mock.calls[0][0].signal.aborted).toBe(true)
  })
})
