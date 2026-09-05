import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const { list, exportCSV, showError, showSuccess } = vi.hoisted(() => ({
  list: vi.fn(),
  exportCSV: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn()
}))

vi.mock('@/api/admin/userAdjustments', () => {
  return {
    userAdjustmentsAPI: { list, exportCSV }
  }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess })
}))

vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key })
}))

import UserAdjustmentsPanel from '../UserAdjustmentsPanel.vue'

const apiResponse = {
  items: [
    {
      id: 101,
      action_id: '00000000-0000-0000-0000-000000000101',
      kind: 'balance' as const,
      operation: 'add' as const,
      requested_value: '12.50000000',
      delta: '12.50000000',
      before_value: '10.00000000',
      after_value: '22.50000000',
      user_id: 7,
      user_email: 'alice@example.com',
      user_name: 'Alice',
      operator_user_id: 1,
      operator_email: 'admin@example.com',
      notes: 'accounting correction',
      request_id: 'request-101',
      client_ip: '203.0.113.10',
      auth_method: 'jwt',
      source: 'admin_action',
      legacy_redeem_code_id: null,
      created_at: '2026-08-11T09:30:00Z'
    },
    {
      id: 100,
      action_id: '00000000-0000-0000-0000-000000000100',
      kind: 'concurrency' as const,
      operation: 'legacy' as const,
      requested_value: null,
      delta: '-2',
      before_value: null,
      after_value: null,
      user_id: 8,
      user_email: null,
      user_name: null,
      operator_user_id: null,
      operator_email: null,
      notes: null,
      request_id: null,
      client_ip: null,
      auth_method: null,
      source: 'legacy_redeem_code',
      legacy_redeem_code_id: 44,
      created_at: '2026-08-10T08:00:00Z'
    }
  ],
  pagination: { page: 1, page_size: 20, total: 2, pages: 1 },
  summary: {
    record_count: '2',
    balance_increase: '12.50000000',
    balance_decrease: '2.50000000',
    balance_net: '10.00000000',
    concurrency_increase: '3',
    concurrency_decrease: '2',
    concurrency_net: '1'
  }
}

function findButton(wrapper: ReturnType<typeof mount>, label: string) {
  const button = wrapper.findAll('button').find((candidate) => candidate.text().includes(label))
  if (!button) throw new Error(`button not found: ${label}`)
  return button
}

describe('UserAdjustmentsPanel', () => {
  beforeEach(() => {
    list.mockReset().mockResolvedValue(apiResponse)
    exportCSV.mockReset().mockResolvedValue(new Blob(['csv'], { type: 'text/csv' }))
    showError.mockReset()
    showSuccess.mockReset()
    Object.defineProperty(window.URL, 'createObjectURL', {
      configurable: true,
      value: vi.fn(() => 'blob:user-adjustments')
    })
    Object.defineProperty(window.URL, 'revokeObjectURL', {
      configurable: true,
      value: vi.fn()
    })
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined)
  })

  afterEach(() => {
    vi.restoreAllMocks()
    document.body.innerHTML = ''
  })

  it('renders mixed balance and concurrency records with full-filter summaries', async () => {
    const wrapper = mount(UserAdjustmentsPanel)
    await flushPromises()

    expect(list).toHaveBeenCalledWith({ page: 1, page_size: 20 })
    expect(wrapper.text()).toContain('alice@example.com')
    expect(wrapper.text()).toContain('admin@example.com')
    expect(wrapper.text()).toContain('+$12.50')
    expect(wrapper.text()).toContain('-2')
    expect(wrapper.text()).toContain('payment.admin.adjustments.summary.balanceNet')
    expect(wrapper.text()).toContain('+$10.00')
    expect(wrapper.text()).toContain('payment.admin.adjustments.summary.concurrencyNet')
    expect(wrapper.text()).toContain('+1')

    await findButton(wrapper, 'common.view').trigger('click')
    await flushPromises()

    expect(document.body.textContent).toContain('00000000-0000-0000-0000-000000000101')
    expect(document.body.textContent).toContain('#101')
    expect(document.body.textContent).toContain('request-101')
    expect(document.body.textContent).toContain('203.0.113.10')
    wrapper.unmount()
  })

  it('keeps batch rows distinct when they share one action id', async () => {
    const sharedActionID = '00000000-0000-0000-0000-000000000999'
    list.mockResolvedValueOnce({
      ...apiResponse,
      items: apiResponse.items.map((item) => ({ ...item, action_id: sharedActionID }))
    })

    const wrapper = mount(UserAdjustmentsPanel)
    await flushPromises()

    expect(wrapper.find('[data-row-id="101"]').exists()).toBe(true)
    expect(wrapper.find('[data-row-id="100"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('uses the same keyword and local time range for search and full CSV export', async () => {
    const wrapper = mount(UserAdjustmentsPanel)
    await flushPromises()
    list.mockClear()

    await wrapper.get('input[type="text"]').setValue('alice@example.com')
    const timeInputs = wrapper.findAll<HTMLInputElement>('input[type="datetime-local"]')
    await timeInputs[0].setValue('2026-08-11T17:00')
    await timeInputs[1].setValue('2026-08-11T18:30')
    await findButton(wrapper, 'common.search').trigger('click')
    await flushPromises()

    const expectedFilters = {
      keyword: 'alice@example.com',
      start_time: new Date('2026-08-11T17:00').toISOString(),
      end_time: new Date('2026-08-11T18:30').toISOString()
    }
    expect(list).toHaveBeenLastCalledWith({ ...expectedFilters, page: 1, page_size: 20 })

    await findButton(wrapper, 'payment.admin.adjustments.exportCsv').trigger('click')
    await flushPromises()

    expect(exportCSV).toHaveBeenCalledWith(expectedFilters)
    expect(window.URL.createObjectURL).toHaveBeenCalledOnce()
    expect(window.URL.revokeObjectURL).toHaveBeenCalledWith('blob:user-adjustments')
    expect(showSuccess).toHaveBeenCalledWith('payment.admin.adjustments.exportSuccess')
    wrapper.unmount()
  })

  it('rejects an inverted time range before requesting or exporting data', async () => {
    const wrapper = mount(UserAdjustmentsPanel)
    await flushPromises()
    list.mockClear()

    const timeInputs = wrapper.findAll<HTMLInputElement>('input[type="datetime-local"]')
    await timeInputs[0].setValue('2026-08-11T19:00')
    await timeInputs[1].setValue('2026-08-11T18:00')
    await findButton(wrapper, 'common.search').trigger('click')

    expect(list).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledWith('payment.admin.adjustments.errors.invalidTimeRange')
    wrapper.unmount()
  })

  it('renders friendly labels for every current administrator source', async () => {
    const sourceCases = [
      ['admin_balance', 'adminBalance'],
      ['admin_user_update', 'adminUserUpdate'],
      ['admin_batch_concurrency', 'adminBatchConcurrency'],
      ['admin_batch_limits', 'adminBatchLimits']
    ] as const
    list.mockResolvedValueOnce({
      ...apiResponse,
      items: sourceCases.map(([source], index) => ({
        ...apiResponse.items[0],
        id: 200 + index,
        action_id: `00000000-0000-0000-0000-00000000020${index}`,
        source
      })),
      pagination: { page: 1, page_size: 20, total: sourceCases.length, pages: 1 }
    })
    const wrapper = mount(UserAdjustmentsPanel)
    await flushPromises()

    const viewButtons = wrapper.findAll('button').filter((button) =>
      button.text().includes('common.view')
    )
    expect(viewButtons).toHaveLength(sourceCases.length)

    for (const [index, [, labelKey]] of sourceCases.entries()) {
      await viewButtons[index].trigger('click')
      await flushPromises()
      expect(document.body.textContent).toContain(
        `payment.admin.adjustments.sources.${labelKey}`
      )
    }
    wrapper.unmount()
  })
})
