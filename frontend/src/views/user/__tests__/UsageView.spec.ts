import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import UsageView from '../UsageView.vue'
import Select, { type SelectOption } from '@/components/common/Select.vue'

const {
  query,
  getStats,
  getDashboardModels,
  getDashboardSnapshotV2,
  listMyErrorRequests,
  list,
  getAvailable,
  showError,
  showWarning,
  showSuccess,
  showInfo,
} = vi.hoisted(() => ({
  query: vi.fn(),
  getStats: vi.fn(),
  getDashboardModels: vi.fn(),
  getDashboardSnapshotV2: vi.fn(),
  listMyErrorRequests: vi.fn(),
  list: vi.fn(),
  getAvailable: vi.fn(),
  showError: vi.fn(),
  showWarning: vi.fn(),
  showSuccess: vi.fn(),
  showInfo: vi.fn(),
}))

const messages: Record<string, string> = {
  'admin.dashboard.timeRange': 'Time range',
  'admin.dashboard.granularity': 'Granularity',
  'admin.dashboard.day': 'Day',
  'admin.dashboard.hour': 'Hour',
  'admin.users.columnSettings': 'Columns',
  'admin.usage.group': 'Group',
  'admin.usage.billingType': 'Billing type',
  'admin.usage.billingMode': 'Billing mode',
  'admin.usage.allTypes': 'All types',
  'admin.usage.allBillingTypes': 'All billing types',
  'admin.usage.billingTypeBalance': 'Balance',
  'admin.usage.billingTypeSubscription': 'Subscription',
  'admin.usage.allBillingModes': 'All billing modes',
  'admin.usage.billingModeToken': 'Token',
  'admin.usage.billingModePerRequest': 'Per request',
  'admin.usage.billingModeImage': 'Image',
  'admin.usage.allGroups': 'All groups',
  'admin.usage.allModels': 'All models',
  'usage.allApiKeys': 'All API Keys',
  'usage.errors.allKeys': 'All API Keys',
  'usage.tabs.usage': 'Usage records',
  'usage.tabs.errors': 'Error records',
  'usage.apiKeyFilter': 'API Key',
  'usage.model': 'Model',
  'usage.type': 'Type',
  'usage.ws': 'WS',
  'usage.stream': 'Stream',
  'usage.sync': 'Sync',
  'usage.compactionFilter': 'Request Kind',
  'usage.allCompactionTypes': 'All Requests',
  'usage.compactionOnly': 'Compaction Only',
  'usage.exporting': 'Exporting',
  'usage.exportCsv': 'Export CSV',
  'usage.failedToLoad': 'Failed to load',
  'usage.noDataToExport': 'No data',
  'usage.preparingExport': 'Preparing export',
  'usage.exportSuccess': 'Export success',
  'usage.exportFailed': 'Export failed',
  'common.refresh': 'Refresh',
  'common.reset': 'Reset',
}

vi.mock('@/api', () => ({
  usageAPI: {
    query,
    getStats,
    getDashboardModels,
    getDashboardSnapshotV2,
    listMyErrorRequests,
  },
  keysAPI: {
    list,
  },
  userGroupsAPI: {
    getAvailable,
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError, showWarning, showSuccess, showInfo,
    cachedPublicSettings: { allow_user_view_error_requests: true },
  }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => messages[key] ?? key,
    }),
  }
})

const simpleStub = { template: '<div><slot /></div>' }
const chartStub = { template: '<div />' }

const usageLog = {
  id: 1,
  request_id: 'req-user-export',
  actual_cost: 0.092883,
  total_cost: 0.092883,
  rate_multiplier: 1,
  service_tier: 'priority',
  input_cost: 0.020285,
  output_cost: 0.00303,
  cache_creation_cost: 0.000001,
  cache_read_cost: 0.069568,
  input_tokens: 4057,
  output_tokens: 101,
  cache_creation_tokens: 4,
  cache_read_tokens: 278272,
  cache_creation_5m_tokens: 0,
  cache_creation_1h_tokens: 0,
  image_count: 0,
  image_size: null,
  first_token_ms: 12,
  duration_ms: 345,
  created_at: '2026-03-08T00:00:00Z',
  model: 'gpt-5.4',
  reasoning_effort: null,
  ip_address: '203.0.113.10',
  api_key: { name: 'demo-key' },
  billing_mode: 'token',
  request_type: 'sync',
  stream: false,
  native_compaction_v2: false,
}

function mountUsageView() {
  return mount(UsageView, {
    global: {
      stubs: {
        AppLayout: simpleStub,
        Pagination: true,
        Select: true,
        DateRangePicker: true,
        Icon: true,
        UsageStatsCards: chartStub,
        UsageTable: chartStub,
        UserErrorRequestsTable: chartStub,
        ModelDistributionChart: chartStub,
        GroupDistributionChart: chartStub,
        EndpointDistributionChart: chartStub,
        TokenUsageTrend: chartStub,
      },
    },
  })
}

describe('user UsageView', () => {
  beforeEach(() => {
    query.mockReset()
    getStats.mockReset()
    getDashboardModels.mockReset()
    getDashboardSnapshotV2.mockReset()
    listMyErrorRequests.mockReset()
    list.mockReset()
    getAvailable.mockReset()
    showError.mockReset()
    showWarning.mockReset()
    showSuccess.mockReset()
    showInfo.mockReset()

    query.mockResolvedValue({ items: [usageLog], total: 1, pages: 1 })
    getStats.mockResolvedValue({
      total_requests: 1,
      total_input_tokens: 10,
      total_output_tokens: 20,
      total_cache_tokens: 0,
      total_tokens: 30,
      total_cost: 0.1,
      total_actual_cost: 0.08,
      average_duration_ms: 12,
      endpoints: [],
      upstream_endpoints: [],
      endpoint_paths: [],
    })
    getDashboardModels.mockResolvedValue({
      models: [{ model: 'gpt-5.4', requests: 1, input_tokens: 10, output_tokens: 20, cache_creation_tokens: 0, cache_read_tokens: 0, total_tokens: 30, cost: 0.1, actual_cost: 0.08 }],
      start_date: '2026-03-08',
      end_date: '2026-03-08',
    })
    getDashboardSnapshotV2.mockResolvedValue({
      generated_at: '2026-03-08T00:00:00Z',
      start_date: '2026-03-08',
      end_date: '2026-03-08',
      granularity: 'hour',
      trend: [],
      groups: [],
    })
    listMyErrorRequests.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, pages: 0 })
    list.mockResolvedValue({ items: [{ id: 1, name: 'demo-key' }], total: 1, page: 1, page_size: 100, pages: 1 })
    getAvailable.mockResolvedValue([{ id: 1, name: 'default' }])
  })

  it('loads logs, stats, model stats, and snapshot on first render', async () => {
    mountUsageView()
    await flushPromises()

    expect(query).toHaveBeenCalled()
    expect(getStats).toHaveBeenCalled()
    expect(getDashboardModels).toHaveBeenCalled()
    expect(getDashboardSnapshotV2).toHaveBeenCalledWith(expect.objectContaining({
      include_trend: true,
      include_model_stats: false,
      include_group_stats: true,
    }), expect.objectContaining({ signal: expect.any(AbortSignal) }))
    expect(list).toHaveBeenCalledTimes(1)
    expect(list).toHaveBeenCalledWith(1, 100)
    expect(getAvailable).toHaveBeenCalled()
  })

  it('clears old-range totals immediately and never restores a late response', async () => {
    const wrapper = mountUsageView()
    await flushPromises()
    expect((wrapper.vm as any).usageStats.total_requests).toBe(1)

    let resolveOld!: (value: any) => void
    let rejectLatest!: (reason: Error) => void
    getStats.mockImplementationOnce(() => new Promise((resolve) => { resolveOld = resolve }))
    getStats.mockImplementationOnce(() => new Promise((_, reject) => { rejectLatest = reject }))

    ;(wrapper.vm as any).onDateRangeChange({ startDate: '2026-09-01T12:34', endDate: '2026-09-08T12:34', preset: null })
    expect((wrapper.vm as any).usageStats).toBeNull()
    expect((wrapper.vm as any).pagination.total).toBeNull()
    const oldSignal = getStats.mock.calls.at(-1)![2].signal as AbortSignal
    ;(wrapper.vm as any).onDateRangeChange({ startDate: '2026-09-08T12:34', endDate: '2026-09-09T12:34', preset: null })
    expect(oldSignal.aborted).toBe(true)
    resolveOld({ total_requests: 777, total_actual_cost: 999 })
    rejectLatest(new Error('statistics timeout'))
    await flushPromises()
    expect((wrapper.vm as any).usageStats).toBeNull()
    expect((wrapper.vm as any).statsError).toBe(true)
    expect((wrapper.vm as any).usageLogs).toHaveLength(1)
    expect(wrapper.text()).toContain('usage.queryFailed')
    wrapper.unmount()
  })

  it('rejects incomplete ranges before changing the query or canceling its requests', async () => {
    const wrapper = mountUsageView()
    await flushPromises()
    const vm = wrapper.vm as any
    const original = { start: vm.startDate, end: vm.endDate, query: vm.normalizedFilters }
    const signal = getStats.mock.calls.at(-1)![2].signal as AbortSignal
    query.mockClear()
    vm.onDateRangeChange({ startDate: '', endDate: '2026-09-08T21:00', preset: null })
    expect(vm.startDate).toBe(original.start)
    expect(vm.endDate).toBe(original.end)
    expect(vm.normalizedFilters).toBe(original.query)
    expect(signal.aborted).toBe(false)
    expect(query).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalled()
    wrapper.unmount()
  })

  it('demotes a stale cached count when the current page proves more rows exist', async () => {
    getStats.mockResolvedValue({ total_requests: 20 })
    query.mockResolvedValue({ items: Array.from({ length: 20 }, (_, index) => ({ ...usageLog, id: index + 1 })), total: null, pages: null, has_more: true, total_exact: false })
    const wrapper = mountUsageView()
    await flushPromises()
    expect((wrapper.vm as any).pagination).toMatchObject({ total: null, has_more: true })
    ;(wrapper.vm as any).handlePageChange(2)
    await flushPromises()
    expect(query.mock.calls.at(-1)![0].page).toBe(2)
    wrapper.unmount()
  })

  it('shows deferred page rows before the exact count and shares the full minute query', async () => {
    let resolveStats!: (value: any) => void
    getStats.mockImplementation(() => new Promise((resolve) => { resolveStats = resolve }))
    query.mockResolvedValue({ items: [usageLog], total: null, pages: null, has_more: true, total_exact: false })
    const wrapper = mountUsageView()
    await flushPromises()
    expect((wrapper.vm as any).usageLogs).toHaveLength(1)
    expect((wrapper.vm as any).loading).toBe(false)
    expect((wrapper.vm as any).pagination).toMatchObject({ total: null, has_more: true })

    ;(wrapper.vm as any).filters.model = 'gpt-5.4'
    ;(wrapper.vm as any).filters.billing_mode = 'token'
    ;(wrapper.vm as any).onDateRangeChange({ startDate: '2026-09-08T12:34', endDate: '2026-09-08T12:35', preset: null })
    await flushPromises()
    const logQuery = query.mock.calls.at(-1)![0]
    const expected = { model: 'gpt-5.4', billing_mode: 'token', start_time: logQuery.start_time, end_time: logQuery.end_time, timezone: logQuery.timezone }
    expect(new Date(logQuery.end_time).getTime() - new Date(logQuery.start_time).getTime()).toBe(60_000)
    expect(getStats.mock.calls.at(-1)![0]).toMatchObject(expected)
    expect(getDashboardModels.mock.calls.at(-1)![0]).toMatchObject(expected)
    expect(getDashboardSnapshotV2.mock.calls.at(-1)![0]).toMatchObject(expected)
    resolveStats({ total_requests: 47, total_actual_cost: 2 })
    await flushPromises()
    expect((wrapper.vm as any).pagination.total).toBe(47)
    wrapper.unmount()
  })

  it('aborts pending errors on a new range and on leaving the page', async () => {
    const wrapper = mountUsageView()
    await flushPromises()
    let resolveOld!: (value: any) => void
    listMyErrorRequests.mockImplementationOnce(() => new Promise((resolve) => { resolveOld = resolve }))
    ;(wrapper.vm as any).switchToErrors()
    const oldSignal = listMyErrorRequests.mock.calls.at(-1)![1].signal as AbortSignal
    ;(wrapper.vm as any).onDateRangeChange({ startDate: '2026-09-01T12:34', endDate: '2026-09-08T12:34', preset: null })
    resolveOld({ items: [{ id: 999 }], total: 1 })
    await flushPromises()
    expect(oldSignal.aborted).toBe(true)
    expect((wrapper.vm as any).errorRows).toEqual([])
    const signals = [getStats.mock.calls.at(-1)![2].signal, getDashboardSnapshotV2.mock.calls.at(-1)![1].signal, listMyErrorRequests.mock.calls.at(-1)![1].signal]
    wrapper.unmount()
    expect(signals.every((signal: AbortSignal) => signal.aborted)).toBe(true)
  })

  it('keeps same-query totals only while refreshing, then clears them after failure', async () => {
    const wrapper = mountUsageView()
    await flushPromises()
    let rejectRefresh!: (error: Error) => void
    getStats.mockImplementationOnce(() => new Promise((_, reject) => { rejectRefresh = reject }))
    ;(wrapper.vm as any).refreshData()
    expect((wrapper.vm as any).usageStats.total_requests).toBe(1)
    expect((wrapper.vm as any).endpointStatsLoading).toBe(true)
    rejectRefresh(new Error('refresh timeout'))
    await flushPromises()
    expect((wrapper.vm as any).usageStats).toBeNull()
    expect((wrapper.vm as any).pagination.total).toBeNull()
    expect((wrapper.vm as any).statsError).toBe(true)
    wrapper.unmount()
  })

  it('refreshes every statistics region with the same force flag', async () => {
    const wrapper = mountUsageView()
    await flushPromises()
    ;(wrapper.vm as any).refreshData()
    await flushPromises()
    expect(getStats.mock.calls.at(-1)![0].force_refresh).toBe(true)
    expect(getDashboardModels.mock.calls.at(-1)![0].force_refresh).toBe(true)
    expect(getDashboardSnapshotV2.mock.calls.at(-1)![0].force_refresh).toBe(true)
    wrapper.unmount()
  })

  it('includes API keys after the first page in both record filters and queries by the selected key', async () => {
    const firstPageKeys = Array.from({ length: 100 }, (_, index) => ({
      id: index + 1,
      name: `key-${index + 1}`,
    }))
    const laterKey = { id: 101, name: 'key-from-second-page' }
    list
      .mockResolvedValueOnce({ items: firstPageKeys, total: 101, page: 1, page_size: 100, pages: 2 })
      .mockResolvedValueOnce({ items: [laterKey], total: 101, page: 2, page_size: 100, pages: 2 })

    const wrapper = mountUsageView()
    await flushPromises()

    expect(list.mock.calls).toEqual([[1, 100], [2, 100]])
    const usageKeySelect = wrapper.findAllComponents(Select).find((select) =>
      select.props('options').some((option: SelectOption) => option.label === 'All API Keys')
    )!
    expect(usageKeySelect.props('options')).toHaveLength(102)
    expect(usageKeySelect.props('options')).toContainEqual({ value: laterKey.id, label: laterKey.name })

    query.mockClear()
    usageKeySelect.vm.$emit('update:modelValue', laterKey.id)
    usageKeySelect.vm.$emit('change', laterKey.id)
    await flushPromises()

    expect(query).toHaveBeenCalledWith(
      expect.objectContaining({ api_key_id: laterKey.id, page: 1 }),
      expect.anything()
    )

    await wrapper.findAll('button').find((button) => button.text() === 'Error records')!.trigger('click')
    await flushPromises()

    const errorKeySelect = wrapper.findAllComponents(Select).find((select) =>
      select.props('options').some((option: SelectOption) => option.label === 'All API Keys')
    )!
    expect(errorKeySelect.props('options')).toHaveLength(102)
    expect(errorKeySelect.props('options')).toContainEqual({ value: laterKey.id, label: laterKey.name })

    listMyErrorRequests.mockClear()
    errorKeySelect.vm.$emit('update:modelValue', laterKey.id)
    errorKeySelect.vm.$emit('change', laterKey.id)
    await flushPromises()

    expect(listMyErrorRequests).toHaveBeenCalledWith(
      expect.objectContaining({ api_key_id: laterKey.id, page: 1 }),
      expect.anything()
    )
    expect(list).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })

  it('does not request another API key page when the user has no keys', async () => {
    list.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 100, pages: 0 })

    const wrapper = mountUsageView()
    await flushPromises()

    expect(list.mock.calls).toEqual([[1, 100]])
    const keySelect = wrapper.findAllComponents(Select).find((select) =>
      select.props('options').some((option: SelectOption) => option.label === 'All API Keys')
    )!
    expect(keySelect.props('options')).toEqual([{ value: null, label: 'All API Keys' }])
    expect(getAvailable).toHaveBeenCalled()
    wrapper.unmount()
  })

  it('stops loading API keys when a later page is empty despite an outdated page count', async () => {
    const firstPageKeys = Array.from({ length: 100 }, (_, index) => ({
      id: index + 1,
      name: `key-${index + 1}`,
    }))
    list
      .mockResolvedValueOnce({ items: firstPageKeys, total: 201, page: 1, page_size: 100, pages: 3 })
      .mockResolvedValueOnce({ items: [], total: 201, page: 2, page_size: 100, pages: 3 })

    const wrapper = mountUsageView()
    await flushPromises()

    expect(list.mock.calls).toEqual([[1, 100], [2, 100]])
    const keySelect = wrapper.findAllComponents(Select).find((select) =>
      select.props('options').some((option: SelectOption) => option.label === 'All API Keys')
    )!
    expect(keySelect.props('options')).toHaveLength(101)
    expect(keySelect.props('options')).toContainEqual({ value: 100, label: 'key-100' })
    wrapper.unmount()
  })

  it('propagates and resets the native compaction filter across page requests', async () => {
    const wrapper = mountUsageView()
    await flushPromises()

    expect((wrapper.vm as any).compactionOptions).toEqual([
      { value: null, label: 'All Requests' },
      { value: true, label: 'Compaction Only' },
    ])

    query.mockClear()
    getStats.mockClear()
    getDashboardModels.mockClear()
    getDashboardSnapshotV2.mockClear()

    ;(wrapper.vm as any).filters.native_compaction_v2 = true
    ;(wrapper.vm as any).applyFilters()
    await flushPromises()

    expect(query).toHaveBeenCalledWith(
      expect.objectContaining({ native_compaction_v2: true }),
      expect.anything()
    )
    expect(getStats).toHaveBeenCalledWith(expect.objectContaining({ native_compaction_v2: true }), undefined, expect.anything())
    expect(getDashboardModels).toHaveBeenCalledWith(expect.objectContaining({ native_compaction_v2: true }), expect.anything())
    expect(getDashboardSnapshotV2).toHaveBeenCalledWith(expect.objectContaining({ native_compaction_v2: true }), expect.anything())

    query.mockClear()
    getStats.mockClear()
    getDashboardModels.mockClear()
    getDashboardSnapshotV2.mockClear()

    ;(wrapper.vm as any).resetFilters()
    await flushPromises()

    expect((wrapper.vm as any).filters.native_compaction_v2).toBeNull()
    expect(query).toHaveBeenCalledWith(
      expect.objectContaining({ native_compaction_v2: null }),
      expect.anything()
    )
    expect(getStats).toHaveBeenCalledWith(expect.objectContaining({ native_compaction_v2: null }), undefined, expect.anything())
    expect(getDashboardModels).toHaveBeenCalledWith(expect.objectContaining({ native_compaction_v2: null }), expect.anything())
    expect(getDashboardSnapshotV2).toHaveBeenCalledWith(expect.objectContaining({ native_compaction_v2: null }), expect.anything())
  })

  it('exports csv with current filters and without admin-only fields', async () => {
    const wrapper = mountUsageView()
    await flushPromises()
    ;(wrapper.vm as any).filters.native_compaction_v2 = true
    ;(wrapper.vm as any).applyFilters()
    await flushPromises()

    let exportedBlob: Blob | null = null
    let csvContent = ''
    const OriginalBlob = globalThis.Blob
    vi.stubGlobal('Blob', vi.fn((parts: BlobPart[], options?: BlobPropertyBag) => {
      csvContent = parts.map((part) => String(part)).join('')
      return new OriginalBlob(parts, options)
    }))
    const originalCreateObjectURL = window.URL.createObjectURL
    const originalRevokeObjectURL = window.URL.revokeObjectURL
    window.URL.createObjectURL = vi.fn((blob: Blob | MediaSource) => {
      exportedBlob = blob as Blob
      return 'blob:usage-export'
    }) as typeof window.URL.createObjectURL
    window.URL.revokeObjectURL = vi.fn(() => {}) as typeof window.URL.revokeObjectURL
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    await (wrapper.vm as any).exportToCSV()

    expect(exportedBlob).not.toBeNull()
    expect(query).toHaveBeenCalledWith(expect.objectContaining({
      page_size: 100,
      sort_by: 'created_at',
      sort_order: 'desc',
      native_compaction_v2: true,
    }), expect.anything())
    expect(clickSpy).toHaveBeenCalled()
    expect(showSuccess).toHaveBeenCalled()
    expect(csvContent.startsWith('\uFEFF')).toBe(true)
    expect(csvContent.slice(1)).toBe([
      'Time,API Key Name,Model,Reasoning Effort,Inbound Endpoint,IP Address,Type,Billing Mode,Input Tokens,Output Tokens,Cache Read Tokens,Cache Creation Tokens,Rate Multiplier,Billed Cost,Original Cost,First Token (ms),Duration (ms)',
      '2026-03-08T00:00:00Z,demo-key,gpt-5.4,"\'-",,203.0.113.10,Sync,Token,4057,101,278272,4,1,0.09288300,0.09288300,12,345',
    ].join('\n'))
    expect(csvContent).toContain('IP Address')
    expect(csvContent).toContain('203.0.113.10')
    expect(csvContent).toContain('Billed Cost')
    expect(csvContent).toContain('Original Cost')
    expect(csvContent).not.toContain('Upstream Endpoint')
    expect(csvContent).not.toContain('account_cost')
    expect(csvContent).not.toContain('account_rate_multiplier')

    window.URL.createObjectURL = originalCreateObjectURL
    window.URL.revokeObjectURL = originalRevokeObjectURL
    vi.unstubAllGlobals()
    clickSpy.mockRestore()
  })

  it('exports historical image rows with image billing mode derived from image_count', async () => {
    query.mockResolvedValue({
      items: [
        {
          ...usageLog,
          request_id: 'req-user-export-legacy-image',
          actual_cost: 0.2,
          total_cost: 0.2,
          input_cost: 0,
          output_cost: 0,
          cache_creation_cost: 0,
          cache_read_cost: 0,
          input_tokens: 0,
          output_tokens: 0,
          cache_creation_tokens: 0,
          cache_read_tokens: 0,
          image_count: 1,
          model: 'gpt-image-2',
          billing_mode: null,
          ip_address: null,
        },
      ],
      total: 1,
      pages: 1,
    })

    const wrapper = mountUsageView()
    await flushPromises()

    let csvContent = ''
    const OriginalBlob = globalThis.Blob
    vi.stubGlobal('Blob', vi.fn((parts: BlobPart[], options?: BlobPropertyBag) => {
      csvContent = parts.map((part) => String(part)).join('')
      return new OriginalBlob(parts, options)
    }))
    const originalCreateObjectURL = window.URL.createObjectURL
    const originalRevokeObjectURL = window.URL.revokeObjectURL
    window.URL.createObjectURL = vi.fn(() => 'blob:usage-export') as typeof window.URL.createObjectURL
    window.URL.revokeObjectURL = vi.fn(() => {}) as typeof window.URL.revokeObjectURL
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    await (wrapper.vm as any).exportToCSV()

    expect(csvContent).toContain('Billing Mode')
    expect(csvContent).toContain('Image')
    expect(csvContent).not.toContain(',Token,0,0,0,0,')

    window.URL.createObjectURL = originalCreateObjectURL
    window.URL.revokeObjectURL = originalRevokeObjectURL
    vi.unstubAllGlobals()
    clickSpy.mockRestore()
  })
})
