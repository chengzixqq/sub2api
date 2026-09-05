import { apiClient } from '../client'

export type UserAdjustmentKind = 'balance' | 'concurrency'
export type UserAdjustmentOperation = 'add' | 'subtract' | 'set' | 'legacy'
export type UserAdjustmentDirection = 'increase' | 'decrease'

export interface UserAdjustment {
  id: number
  action_id: string
  kind: UserAdjustmentKind
  operation: UserAdjustmentOperation
  requested_value: string | null
  delta: string
  before_value: string | null
  after_value: string | null
  user_id: number | null
  user_email: string | null
  user_name: string | null
  operator_user_id: number | null
  operator_email: string | null
  operator_name: string | null
  notes: string | null
  request_id: string | null
  client_ip: string | null
  auth_method: string | null
  source: string
  legacy_redeem_code_id: number | null
  created_at: string
}

export interface UserAdjustmentSummary {
  record_count: string
  balance_increase: string
  balance_decrease: string
  balance_net: string
  concurrency_increase: string
  concurrency_decrease: string
  concurrency_net: string
}

export interface UserAdjustmentPagination {
  page: number
  page_size: number
  total: number
  pages: number
}

export interface UserAdjustmentListResponse {
  items: UserAdjustment[]
  pagination: UserAdjustmentPagination
  summary: UserAdjustmentSummary
}

export interface UserAdjustmentQuery {
  page?: number
  page_size?: number
  keyword?: string
  operator?: string
  kind?: UserAdjustmentKind
  operation?: UserAdjustmentOperation
  direction?: UserAdjustmentDirection
  start_time?: string
  end_time?: string
}

export type UserAdjustmentExportQuery = Omit<UserAdjustmentQuery, 'page' | 'page_size'>

export async function list(
  params: UserAdjustmentQuery = {}
): Promise<UserAdjustmentListResponse> {
  type RawResponse = Omit<UserAdjustmentListResponse, 'pagination' | 'summary'> & {
    pagination?: UserAdjustmentPagination
    total?: number
    page?: number
    page_size?: number
    pages?: number
    summary: Omit<UserAdjustmentSummary, 'record_count'> & { record_count: string | number }
  }
  const { data } = await apiClient.get<RawResponse>('/admin/user-adjustments', {
    params
  })
  return {
    items: data.items || [],
    pagination: data.pagination || {
      total: data.total || 0,
      page: data.page || params.page || 1,
      page_size: data.page_size || params.page_size || 20,
      pages: data.pages || 1
    },
    summary: {
      ...data.summary,
      record_count: String(data.summary?.record_count ?? 0)
    }
  }
}

export async function exportCSV(params: UserAdjustmentExportQuery = {}): Promise<Blob> {
  const { data } = await apiClient.get<Blob>('/admin/user-adjustments/export', {
    params,
    responseType: 'blob'
  })
  return data
}

export const userAdjustmentsAPI = {
  list,
  exportCSV
}

export default userAdjustmentsAPI
