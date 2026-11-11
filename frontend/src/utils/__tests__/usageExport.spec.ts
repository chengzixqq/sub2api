import { afterEach, describe, expect, it, vi } from 'vitest'
import { queryUsageExportPage, serializeUsageBillingDetails, BILLING_DETAIL_EXPORT_HEADERS, usageBillingDetailExportRows } from '../usageExport'
import * as XLSX from 'xlsx'
import type { AdminUsageBillingDetails } from '@/types'

const details: AdminUsageBillingDetails = {
  version: 1,
  status: 'partial_unpriced',
  components: [{
    role: 'advisor', iteration_index: 0, model: 'claude-opus-advisor', billing_mode: 'token',
    input_tokens: 20, output_tokens: 10, cache_creation_tokens: 7, cache_read_tokens: 5,
    cache_creation_5m_tokens: 3, cache_creation_1h_tokens: 4,
    input_cost: null, output_cost: null, cache_creation_cost: null, cache_read_cost: null,
    total_cost: null, actual_cost: null, account_stats_cost: 0.12345, pricing_source: 'unknown',
    unpriced_reason: 'advisor_pricing_unavailable',
  }],
}

afterEach(() => vi.useRealTimers())

describe('usage export billing projection', () => {
  it('keeps public per-model usage and unknown prices while dropping internal fields', () => {
    const supplied = { ...details, internal_mapping: 'private', components: [{ ...details.components[0], secret_rule: 'private' }] }
    const result = JSON.parse(serializeUsageBillingDetails(supplied))
    expect(result.status).toBe('partial_unpriced')
    expect(result.components[0]).toMatchObject({ model: 'claude-opus-advisor', iteration_index: 0, input_tokens: 20, total_cost: null })
    expect(result.components[0]).not.toHaveProperty('account_stats_cost')
    expect(result.components[0]).not.toHaveProperty('pricing_source')
    expect(JSON.stringify(result)).not.toContain('private')
  })

  it('includes permitted account pricing fields only for admin exports', () => {
    expect(JSON.parse(serializeUsageBillingDetails(details, true)).components[0]).toMatchObject({ account_stats_cost: 0.12345, pricing_source: 'unknown' })
    expect(serializeUsageBillingDetails(null)).toBe('')
  })

  it('preserves OpenAI cache buckets in public JSON and defaults historical components to zero', () => {
    const ttlDetails = { ...details, components: [{ ...details.components[0]!, cache_creation_tokens: 37, cache_creation_30m_tokens: 11, cache_creation_12h_tokens: 19 }] }
    const component = JSON.parse(serializeUsageBillingDetails(ttlDetails)).components[0]
    expect(component).toMatchObject({ cache_creation_tokens: 37, cache_creation_30m_tokens: 11, cache_creation_12h_tokens: 19 })
    expect(component).not.toHaveProperty('account_stats_cost')
    expect(JSON.parse(serializeUsageBillingDetails(details)).components[0]).toMatchObject({ cache_creation_30m_tokens: 0, cache_creation_12h_tokens: 0 })
  })
})

describe('billing detail worksheet serialization', () => {
  it('round-trips 100 components without hitting the XLSX cell limit or losing detail', () => {
    const largeDetails = {
      ...details,
      components: Array.from({ length: 100 }, (_, index) => ({
        ...details.components[0]!, iteration_index: index === 1 ? null : index,
        input_cost: 0, output_cost: 0.00000022, secret_rule: 'must-never-export',
        cache_creation_30m_tokens: 11, cache_creation_12h_tokens: 19,
      })),
    }
    expect(JSON.stringify(largeDetails).length).toBeGreaterThan(32767)
    const rows = usageBillingDetailExportRows({
      id: 42, request_id: 'req-many-advisors', created_at: '2026-11-10T00:00:00Z', billing_details: largeDetails,
    }, 'Not priced')
    const sheet = XLSX.utils.aoa_to_sheet([[...BILLING_DETAIL_EXPORT_HEADERS], ...rows])
    const workbook = XLSX.utils.book_new()
    XLSX.utils.book_append_sheet(workbook, sheet, 'Billing Details')
    const bytes = XLSX.write(workbook, { bookType: 'xlsx', type: 'array' })
    const restored = XLSX.read(bytes, { type: 'array' })
    const restoredRows = XLSX.utils.sheet_to_json<(string | number)[]>(restored.Sheets['Billing Details']!, { header: 1, defval: '' })
    expect(restoredRows).toHaveLength(101)
    expect(restoredRows[0]).toEqual([...BILLING_DETAIL_EXPORT_HEADERS])
    expect(restoredRows[1]!.slice(0, 7)).toEqual([42, 'req-many-advisors', '2026-11-10T00:00:00Z', 'partial_unpriced', 1, 'advisor', 0])
    expect(restoredRows[2]![6]).toBe('')
    expect(restoredRows[100]![6]).toBe(99)
    expect(restoredRows[1]![BILLING_DETAIL_EXPORT_HEADERS.indexOf('cache_creation_30m_tokens')]).toBe(11)
    expect(restoredRows[1]![BILLING_DETAIL_EXPORT_HEADERS.indexOf('cache_creation_12h_tokens')]).toBe(19)
    const inputCostIndex = BILLING_DETAIL_EXPORT_HEADERS.indexOf('input_cost')
    expect(restoredRows[1]!.slice(inputCostIndex, inputCostIndex + 3)).toEqual(['0.00000000', '0.00000022', 'Not priced'])
    expect(restoredRows[1]!.slice(-2)).toEqual(['0.12345000', 'unknown'])
    expect(JSON.stringify(restoredRows)).not.toContain('must-never-export')
    const publicDetails = JSON.parse(serializeUsageBillingDetails(largeDetails))
    expect(publicDetails.components).toHaveLength(100)
    for (const component of publicDetails.components) {
      expect(component).not.toHaveProperty('account_stats_cost')
      expect(component).not.toHaveProperty('pricing_source')
      expect(component).not.toHaveProperty('secret_rule')
    }
  })
})

describe('usage export retry', () => {
  it('caps Retry-After at 60 seconds and retries the same request', async () => {
    vi.useFakeTimers()
    const request = vi.fn().mockRejectedValueOnce({ status: 429, retry_after_seconds: 10000 }).mockResolvedValue({ items: [1] })
    const notify = vi.fn()
    const result = queryUsageExportPage(request, new AbortController().signal, notify)
    await vi.advanceTimersByTimeAsync(59999)
    expect(request).toHaveBeenCalledTimes(1)
    expect(notify).toHaveBeenCalledWith(60)
    await vi.advanceTimersByTimeAsync(1)
    await expect(result).resolves.toEqual({ items: [1] })
    expect(request).toHaveBeenCalledTimes(2)
  })

  it('cancels immediately during backoff and does not request the page again', async () => {
    vi.useFakeTimers()
    const controller = new AbortController()
    const request = vi.fn().mockRejectedValue({ status: 429, retry_after_seconds: 30 })
    const result = queryUsageExportPage(request, controller.signal, vi.fn())
    const rejected = expect(result).rejects.toMatchObject({ name: 'AbortError' })
    await vi.advanceTimersByTimeAsync(1)
    controller.abort()
    await rejected
    await vi.advanceTimersByTimeAsync(60000)
    expect(request).toHaveBeenCalledTimes(1)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('does not retry non-rate-limit failures or already cancelled exports', async () => {
    const failure = new Error('Unavailable')
    const request = vi.fn().mockRejectedValue(failure)
    await expect(queryUsageExportPage(request, new AbortController().signal, vi.fn())).rejects.toBe(failure)
    const controller = new AbortController()
    controller.abort()
    await expect(queryUsageExportPage(request, controller.signal, vi.fn())).rejects.toMatchObject({ name: 'AbortError' })
    expect(request).toHaveBeenCalledTimes(1)
  })
})
