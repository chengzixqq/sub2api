import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get } = vi.hoisted(() => ({
  get: vi.fn()
}))

vi.mock('@/api/client', () => ({
  apiClient: { get }
}))

import { exportCSV, list, type UserAdjustmentListResponse } from '@/api/admin/userAdjustments'

const response: UserAdjustmentListResponse = {
  items: [
    {
      id: 12,
      action_id: '00000000-0000-0000-0000-000000000012',
      kind: 'balance',
      operation: 'add',
      requested_value: '10.00000000',
      delta: '10.00000000',
      before_value: '2.00000000',
      after_value: '12.00000000',
      user_id: 4,
      user_email: 'user@example.com',
      user_name: 'User',
      operator_user_id: 1,
      operator_email: 'admin@example.com',
      notes: 'manual top-up',
      request_id: 'request-12',
      client_ip: '127.0.0.1',
      auth_method: 'jwt',
      source: 'admin_action',
      legacy_redeem_code_id: null,
      created_at: '2026-08-11T09:00:00Z'
    }
  ],
  pagination: { page: 1, page_size: 20, total: 1, pages: 1 },
  summary: {
    record_count: '1',
    balance_increase: '10.00000000',
    balance_decrease: '0.00000000',
    balance_net: '10.00000000',
    concurrency_increase: '0',
    concurrency_decrease: '0',
    concurrency_net: '0'
  }
}

describe('admin user adjustments api', () => {
  beforeEach(() => {
    get.mockReset()
  })

  it('lists adjustments with pagination and accounting filters', async () => {
    get.mockResolvedValue({ data: response })
    const params = {
      page: 2,
      page_size: 50,
      keyword: 'user@example.com',
      kind: 'balance' as const,
      operation: 'add' as const,
      start_time: '2026-08-11T09:00:00Z',
      end_time: '2026-08-11T10:00:00Z'
    }

    await expect(list(params)).resolves.toEqual(response)
    expect(get).toHaveBeenCalledWith('/admin/user-adjustments', { params })
  })

  it('exports every matching row as a blob without pagination parameters', async () => {
    const blob = new Blob(['csv'], { type: 'text/csv' })
    get.mockResolvedValue({ data: blob })
    const params = {
      keyword: 'admin@example.com',
      kind: 'concurrency' as const,
      operation: 'set' as const
    }

    await expect(exportCSV(params)).resolves.toBe(blob)
    expect(get).toHaveBeenCalledWith('/admin/user-adjustments/export', {
      params,
      responseType: 'blob'
    })
  })

  it('normalizes the deployed flat pagination shape', async () => {
    const { pagination, ...rest } = response
    get.mockResolvedValue({
      data: {
        ...rest,
        total: pagination.total,
        page: pagination.page,
        page_size: pagination.page_size,
        pages: pagination.pages,
        summary: { ...rest.summary, record_count: 1 }
      }
    })

    const result = await list({ page: 1, page_size: 20 })

    expect(result.pagination).toEqual(pagination)
    expect(result.summary.record_count).toBe('1')
  })
})
