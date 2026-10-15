import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'

const { listAccounts, listWithEtag, getBatchTodayStats, getUpstreamBillingProbeSettings, getAllProxies, getAllGroups } = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  listWithEtag: vi.fn(),
  getBatchTodayStats: vi.fn(),
  getUpstreamBillingProbeSettings: vi.fn(),
  getAllProxies: vi.fn(),
  getAllGroups: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      listWithEtag,
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
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn() })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ token: 'test-token', isSimpleMode: false })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const DataTableStub = {
  props: ['columns', 'data'],
  template: `
    <div>
      <span v-for="column in columns" :key="column.key" :data-column="column.key">{{ column.label }}</span>
      <template v-if="columns.some(column => column.key === 'current_rpm')">
        <div v-for="row in data" :key="row.id" :data-rpm-account="row.id">
          <slot name="cell-current_rpm" :row="row" />
        </div>
      </template>
    </div>
  `
}

const wrappers: ReturnType<typeof mount>[] = []

function mountView() {
  const wrapper = mount(AccountsView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /></div>' },
        DataTable: DataTableStub,
        AccountTableActions: {
          template: '<div><button data-test="refresh" @click="$emit(\'refresh\')" /><slot name="after" /></div>'
        },
        AccountTableFilters: true,
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
        EditAccountModal: true,
        BulkEditAccountModal: true,
        Icon: true,
        Teleport: true
      }
    }
  })
  wrappers.push(wrapper)
  return wrapper
}

const apiKeyAccount = {
  id: 1,
  name: 'API key account',
  platform: 'openai',
  type: 'apikey',
  status: 'active',
  schedulable: true,
  concurrency: 4,
  priority: 0,
  base_rpm: 60,
  updated_at: '2026-10-15T00:00:00Z'
}

const page = (items: object[]) => ({ items, total: items.length, page: 1, page_size: 20, pages: 1 })

describe('admin AccountsView current RPM column', () => {
  beforeEach(() => {
    localStorage.clear()
    listAccounts.mockReset().mockResolvedValue(page([{ ...apiKeyAccount, current_rpm: 37 }]))
    listWithEtag.mockReset().mockResolvedValue({ notModified: true, etag: null, data: null })
    getBatchTodayStats.mockReset().mockResolvedValue({ stats: {} })
    getUpstreamBillingProbeSettings.mockReset().mockResolvedValue({ enabled: true })
    getAllProxies.mockReset().mockResolvedValue([])
    getAllGroups.mockReset().mockResolvedValue([])
  })

  afterEach(() => {
    wrappers.splice(0).forEach(wrapper => wrapper.unmount())
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('shows a configurable current RPM column after capacity for fresh and older saved layouts', async () => {
    localStorage.setItem('account-hidden-columns', JSON.stringify(['today_stats']))
    localStorage.setItem('account-hidden-columns-version', 'scheduler-score-hidden-by-default')
    const wrapper = mountView()
    await flushPromises()

    const keys = wrapper.findAll('[data-column]').map(column => column.attributes('data-column'))
    expect(keys[keys.indexOf('capacity') + 1]).toBe('current_rpm')
    expect(wrapper.get('[data-column="current_rpm"]').text()).toBe('admin.accounts.columns.currentRpm')
    expect(wrapper.get('[data-rpm-account="1"]').text()).toBe('37')

    await wrapper.get('button[title="admin.accounts.moreActions"]').trigger('click')
    const toggle = wrapper.findAll('button').find(button => button.text() === 'admin.accounts.columns.currentRpm')!
    await toggle.trigger('click')

    expect(wrapper.find('[data-column="current_rpm"]').exists()).toBe(false)
    expect(JSON.parse(localStorage.getItem('account-hidden-columns') || '[]')).toContain('current_rpm')
    const remounted = mountView()
    await flushPromises()
    expect(remounted.find('[data-column="current_rpm"]').exists()).toBe(false)
  })

  it('shows current values for every provider and preserves zero without substituting RPM limits', async () => {
    listAccounts.mockResolvedValue(page([
      { ...apiKeyAccount, id: 1, current_rpm: 37 },
      { ...apiKeyAccount, id: 2, platform: 'anthropic', current_rpm: 0 },
      { ...apiKeyAccount, id: 3, platform: 'gemini', current_rpm: 9, base_rpm: null },
      { ...apiKeyAccount, id: 4, current_rpm: null },
      { ...apiKeyAccount, id: 5 }
    ]))
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-rpm-account="1"]').text()).toBe('37')
    expect(wrapper.get('[data-rpm-account="2"]').text()).toBe('0')
    expect(wrapper.get('[data-rpm-account="3"]').text()).toBe('9')
    expect(wrapper.get('[data-rpm-account="4"]').text()).toBe('common.notAvailable')
    expect(wrapper.get('[data-rpm-account="5"]').text()).toBe('common.notAvailable')
  })

  it('updates current RPM from a manual list refresh', async () => {
    const wrapper = mountView()
    await flushPromises()
    listAccounts.mockResolvedValue(page([{ ...apiKeyAccount, current_rpm: 12 }]))

    await wrapper.get('[data-test="refresh"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-rpm-account="1"]').text()).toBe('12')
  })

  it('applies RPM-only automatic updates, minute resets, and unavailable counters', async () => {
    vi.useFakeTimers()
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    localStorage.setItem('account-auto-refresh', JSON.stringify({ enabled: true, interval_seconds: 5 }))
    const wrapper = mountView()
    await flushPromises()

    for (const count of [38, 0, null]) {
      listWithEtag.mockResolvedValue({
        notModified: false,
        etag: `rpm-${count}`,
        data: page([{ ...apiKeyAccount, current_rpm: count }])
      })
      await vi.advanceTimersByTimeAsync(6000)
      await flushPromises()

      expect(wrapper.get('[data-rpm-account="1"]').text()).toBe(count == null ? 'common.notAvailable' : String(count))
    }
    expect(listWithEtag).toHaveBeenCalledWith(1, 20, expect.objectContaining({ lite: '1' }), expect.any(Object))
  })
})
