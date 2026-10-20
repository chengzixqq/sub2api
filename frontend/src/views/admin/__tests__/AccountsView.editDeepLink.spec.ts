import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter, type Router } from 'vue-router'

import AccountsView from '../AccountsView.vue'

const {
  listAccounts,
  listWithEtag,
  getById,
  getBatchTodayStats,
  getUpstreamBillingProbeSettings,
  getAllProxies,
  getAllGroups,
  showError
} = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  listWithEtag: vi.fn(),
  getById: vi.fn(),
  getBatchTodayStats: vi.fn(),
  getUpstreamBillingProbeSettings: vi.fn(),
  getAllProxies: vi.fn(),
  getAllGroups: vi.fn(),
  showError: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      listWithEtag,
      getById,
      getBatchTodayStats,
      getUpstreamBillingProbeSettings,
      delete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn(),
      toggleSchedulable: vi.fn()
    },
    proxies: { getAll: getAllProxies },
    groups: { getAll: getAllGroups }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess: vi.fn(), showInfo: vi.fn() })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ token: 'test-token', isSimpleMode: false })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key)
    })
  }
})

const EditAccountModalStub = {
  props: ['show', 'account', 'proxies', 'groups'],
  template: `
    <div
      data-test="edit-modal"
      :data-show="String(show)"
      :data-account-id="account?.id ?? ''"
      :data-proxy-count="proxies?.length ?? 0"
      :data-group-count="groups?.length ?? 0"
    />
  `
}

const AccountTableFiltersStub = {
  props: ['searchQuery', 'filters', 'groups'],
  template: '<div data-test="filters" :data-search="searchQuery" />'
}

// 渲染每行的操作列，便于模拟用户在等待期间点击别的账号的“编辑”。
const DataTableStub = {
  props: ['data'],
  template: `
    <div>
      <div v-for="row in data" :key="row.id" :data-test="'row-' + row.id">
        <slot name="cell-actions" :row="row" />
      </div>
    </div>
  `
}

const account = {
  id: 12,
  name: 'acc-x',
  platform: 'openai',
  type: 'apikey',
  status: 'active',
  schedulable: true,
  concurrency: 4,
  priority: 0,
  updated_at: '2026-10-15T00:00:00Z'
}

const page = (items: object[]) => ({ items, total: items.length, page: 1, page_size: 20, pages: 1 })

const wrappers: ReturnType<typeof mount>[] = []
const listSearchAtCall: string[] = []

async function createTestRouter(path: string): Promise<Router> {
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/admin/accounts', component: { template: '<div />' } }]
  })
  await router.push(path)
  return router
}

function mountView(router: Router, options: { renderRows?: boolean } = {}) {
  const wrapper = mount(AccountsView, {
    global: {
      plugins: [router],
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /></div>' },
        DataTable: options.renderRows ? DataTableStub : true,
        AccountTableActions: true,
        AccountTableFilters: AccountTableFiltersStub,
        AccountBulkActionsBar: true,
        Pagination: true,
        ConfirmDialog: true,
        AccountActionMenu: true,
        ImportDataModal: true,
        ReAuthAccountModal: true,
        AccountTestModal: true,
        AccountStatsModal: true,
        ScheduledTestsPanel: true,
        SyncFromCrsModal: true,
        TempUnschedStatusModal: true,
        ErrorPassthroughRulesModal: true,
        TLSFingerprintProfilesModal: true,
        CreateAccountModal: true,
        EditAccountModal: EditAccountModalStub,
        BulkEditAccountModal: true,
        Icon: true,
        Teleport: true
      }
    }
  })
  wrappers.push(wrapper)
  return wrapper
}

describe('admin AccountsView edit deep link', () => {
  beforeEach(() => {
    localStorage.clear()
    listSearchAtCall.splice(0)
    listAccounts.mockReset().mockImplementation(async (_page: number, _size: number, params: { search?: string }) => {
      listSearchAtCall.push(params.search ?? '')
      return page([account])
    })
    listWithEtag.mockReset().mockResolvedValue({ notModified: true, etag: null, data: null })
    getById.mockReset().mockResolvedValue({ ...account })
    getBatchTodayStats.mockReset().mockResolvedValue({ stats: {} })
    getUpstreamBillingProbeSettings.mockReset().mockResolvedValue({ enabled: true })
    getAllProxies.mockReset().mockResolvedValue([{ id: 3, name: 'proxy' }])
    getAllGroups.mockReset().mockResolvedValue([{ id: 5, name: 'group' }])
    showError.mockReset()
  })

  afterEach(() => {
    wrappers.splice(0).forEach(wrapper => wrapper.unmount())
    vi.restoreAllMocks()
  })

  it('opens the edit modal for ?edit=<id> and removes the parameter from the URL', async () => {
    const router = await createTestRouter('/admin/accounts?edit=12')
    const wrapper = mountView(router)
    await flushPromises()

    expect(getById).toHaveBeenCalledTimes(1)
    expect(getById).toHaveBeenCalledWith(12)
    const modal = wrapper.get('[data-test="edit-modal"]')
    expect(modal.attributes('data-show')).toBe('true')
    expect(modal.attributes('data-account-id')).toBe('12')
    expect(router.currentRoute.value.path).toBe('/admin/accounts')
    expect(router.currentRoute.value.query).toEqual({})
    expect(showError).not.toHaveBeenCalled()
  })

  it('waits for proxies and groups before opening the editor', async () => {
    let resolveProxies: (value: unknown) => void = () => undefined
    getAllProxies.mockReturnValue(new Promise(resolve => { resolveProxies = resolve }))
    const router = await createTestRouter('/admin/accounts?edit=12')
    const wrapper = mountView(router)
    await flushPromises()

    expect(getById).not.toHaveBeenCalled()
    expect(wrapper.get('[data-test="edit-modal"]').attributes('data-show')).toBe('false')

    resolveProxies([{ id: 3, name: 'proxy' }])
    await flushPromises()

    const modal = wrapper.get('[data-test="edit-modal"]')
    expect(getById).toHaveBeenCalledWith(12)
    expect(modal.attributes('data-show')).toBe('true')
    expect(modal.attributes('data-proxy-count')).toBe('1')
    expect(modal.attributes('data-group-count')).toBe('1')
  })

  it('prefills a trimmed search before the first list request and strips both parameters', async () => {
    const router = await createTestRouter(`/admin/accounts?edit=12&search=${encodeURIComponent('  acc-x  ')}`)
    const wrapper = mountView(router)
    await flushPromises()

    expect(listSearchAtCall[0]).toBe('acc-x')
    expect(wrapper.get('[data-test="filters"]').attributes('data-search')).toBe('acc-x')
    expect(router.currentRoute.value.query).toEqual({})
    expect(wrapper.get('[data-test="edit-modal"]').attributes('data-show')).toBe('true')
  })

  it('prefills search without opening the editor when only ?search= is present', async () => {
    const router = await createTestRouter('/admin/accounts?search=acc-y')
    const wrapper = mountView(router)
    await flushPromises()

    expect(listSearchAtCall[0]).toBe('acc-y')
    expect(getById).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()
    expect(wrapper.get('[data-test="edit-modal"]').attributes('data-show')).toBe('false')
    expect(router.currentRoute.value.query).toEqual({})
  })

  it('shows a hint for an invalid id without requesting account details', async () => {
    const router = await createTestRouter('/admin/accounts?edit=abc')
    const wrapper = mountView(router)
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('admin.accounts.deepLinkInvalidId:{"id":"abc"}')
    expect(getById).not.toHaveBeenCalled()
    expect(wrapper.get('[data-test="edit-modal"]').attributes('data-show')).toBe('false')
    expect(router.currentRoute.value.query).toEqual({})
  })

  it('surfaces the API error when the account is missing or not visible', async () => {
    getById.mockRejectedValue({ status: 404, message: 'account not found' })
    vi.spyOn(console, 'error').mockImplementation(() => undefined)
    const router = await createTestRouter('/admin/accounts?edit=404')
    const wrapper = mountView(router)
    await flushPromises()

    expect(getById).toHaveBeenCalledWith(404)
    expect(showError).toHaveBeenCalledWith('account not found')
    const modal = wrapper.get('[data-test="edit-modal"]')
    expect(modal.attributes('data-show')).toBe('false')
    expect(modal.attributes('data-proxy-count')).toBe('1')
    expect(modal.attributes('data-group-count')).toBe('1')
    expect(router.currentRoute.value.query).toEqual({})
  })

  it('keeps the page behaviour unchanged without deep link parameters', async () => {
    const router = await createTestRouter('/admin/accounts?page=2')
    const replaceSpy = vi.spyOn(router, 'replace')
    const wrapper = mountView(router)
    await flushPromises()

    expect(replaceSpy).not.toHaveBeenCalled()
    expect(getById).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()
    expect(listSearchAtCall[0]).toBe('')
    expect(wrapper.get('[data-test="edit-modal"]').attributes('data-show')).toBe('false')
    expect(router.currentRoute.value.query).toEqual({ page: '2' })
  })

  it('preserves unrelated query parameters when stripping the deep link', async () => {
    const router = await createTestRouter('/admin/accounts?edit=12&foo=bar')
    mountView(router)
    await flushPromises()

    expect(router.currentRoute.value.query).toEqual({ foo: 'bar' })
    expect(getById).toHaveBeenCalledWith(12)
  })

  it('still opens the editor when removing the parameter from the URL fails', async () => {
    const router = await createTestRouter('/admin/accounts?edit=12')
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined)
    vi.spyOn(console, 'error').mockImplementation(() => undefined)
    router.beforeEach(() => {
      throw new Error('navigation failed')
    })
    const wrapper = mountView(router)
    await flushPromises()

    expect(getById).toHaveBeenCalledWith(12)
    expect(wrapper.get('[data-test="edit-modal"]').attributes('data-show')).toBe('true')
    expect(warn).toHaveBeenCalledWith('Failed to clear account deep link query:', expect.any(Error))
  })

  it('does not replace an editor the user opened while proxies and groups were loading', async () => {
    const other = { ...account, id: 5, name: 'acc-5' }
    listAccounts.mockResolvedValue(page([other]))
    getById.mockImplementation(async (id: number) => ({ ...account, id }))
    let resolveProxies: (value: unknown) => void = () => undefined
    getAllProxies.mockReturnValue(new Promise(resolve => { resolveProxies = resolve }))
    const router = await createTestRouter('/admin/accounts?edit=12')
    const wrapper = mountView(router, { renderRows: true })
    await flushPromises()

    await wrapper.get('[data-test="row-5"] button').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-test="edit-modal"]').attributes('data-account-id')).toBe('5')

    resolveProxies([{ id: 3, name: 'proxy' }])
    await flushPromises()

    expect(getById).toHaveBeenCalledTimes(1)
    expect(getById).toHaveBeenCalledWith(5)
    const modal = wrapper.get('[data-test="edit-modal"]')
    expect(modal.attributes('data-show')).toBe('true')
    expect(modal.attributes('data-account-id')).toBe('5')
  })

  it('discards the deep-link account when another editor opened while it was being fetched', async () => {
    const other = { ...account, id: 5, name: 'acc-5' }
    listAccounts.mockResolvedValue(page([other]))
    let resolveDeepLinkAccount: (value: unknown) => void = () => undefined
    getById.mockImplementation((id: number) =>
      id === 12
        ? new Promise(resolve => { resolveDeepLinkAccount = resolve })
        : Promise.resolve({ ...account, id })
    )
    const router = await createTestRouter('/admin/accounts?edit=12')
    const wrapper = mountView(router, { renderRows: true })
    await flushPromises()
    expect(getById).toHaveBeenCalledWith(12)

    await wrapper.get('[data-test="row-5"] button').trigger('click')
    await flushPromises()
    resolveDeepLinkAccount({ ...account, id: 12 })
    await flushPromises()

    const modal = wrapper.get('[data-test="edit-modal"]')
    expect(modal.attributes('data-show')).toBe('true')
    expect(modal.attributes('data-account-id')).toBe('5')
  })

  it('skips the deep link when the page is left before proxies and groups finish loading', async () => {
    let resolveProxies: (value: unknown) => void = () => undefined
    getAllProxies.mockReturnValue(new Promise(resolve => { resolveProxies = resolve }))
    getById.mockRejectedValue({ status: 404, message: 'account not found' })
    const router = await createTestRouter('/admin/accounts?edit=12')
    const wrapper = mountView(router)
    await flushPromises()

    wrappers.splice(wrappers.indexOf(wrapper), 1)
    wrapper.unmount()
    resolveProxies([{ id: 3, name: 'proxy' }])
    await flushPromises()

    expect(getById).not.toHaveBeenCalled()
    expect(showError).not.toHaveBeenCalled()
  })
})
