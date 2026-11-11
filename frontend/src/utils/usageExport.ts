import type { AdminUsageBillingDetails, AdminUsageLog } from '@/types'

export const USAGE_EXPORT_PAGE_SIZE = 1000

function throwIfAborted(signal: AbortSignal): void {
  if (signal.aborted) throw new DOMException('Export cancelled', 'AbortError')
}

function waitForRetry(milliseconds: number, signal: AbortSignal): Promise<void> {
  throwIfAborted(signal)
  return new Promise((resolve, reject) => {
    const onAbort = () => {
      clearTimeout(timer)
      signal.removeEventListener('abort', onAbort)
      reject(new DOMException('Export cancelled', 'AbortError'))
    }
    const timer = setTimeout(() => {
      signal.removeEventListener('abort', onAbort)
      resolve()
    }, milliseconds)
    signal.addEventListener('abort', onAbort, { once: true })
  })
}

/** Retry only the current page, with cancellation covering both requests and backoff. */
export async function queryUsageExportPage<T>(
  request: () => Promise<T>,
  signal: AbortSignal,
  onRateLimited: (seconds: number) => void,
): Promise<T> {
  for (let attempt = 0; ; attempt++) {
    throwIfAborted(signal)
    try {
      const result = await request()
      throwIfAborted(signal)
      return result
    } catch (error: unknown) {
      throwIfAborted(signal)
      const rateLimit = error as { status?: number; retry_after_seconds?: number } | null
      if (rateLimit?.status !== 429 || attempt >= 3) throw error
      const retryAfter = rateLimit.retry_after_seconds
      const seconds = typeof retryAfter === 'number' && Number.isFinite(retryAfter)
        ? Math.min(60, Math.max(1, retryAfter)) : 60
      onRateLimited(seconds)
      await waitForRetry(seconds * 1000, signal)
    }
  }
}

/** Explicit projection keeps ordinary-user exports free of admin/internal metadata. */
export function serializeUsageBillingDetails(
  details: AdminUsageBillingDetails | null | undefined,
  includeAdmin = false,
): string {
  if (!details) return ''
  return JSON.stringify({
    version: details.version,
    status: details.status,
    components: details.components.map(component => ({
      role: component.role,
      iteration_index: component.iteration_index,
      model: component.model,
      billing_mode: component.billing_mode,
      input_tokens: component.input_tokens,
      output_tokens: component.output_tokens,
      cache_creation_tokens: component.cache_creation_tokens,
      cache_read_tokens: component.cache_read_tokens,
      cache_creation_5m_tokens: component.cache_creation_5m_tokens,
      cache_creation_1h_tokens: component.cache_creation_1h_tokens,
      cache_creation_30m_tokens: component.cache_creation_30m_tokens ?? 0,
      cache_creation_12h_tokens: component.cache_creation_12h_tokens ?? 0,
      input_cost: component.input_cost,
      output_cost: component.output_cost,
      cache_creation_cost: component.cache_creation_cost,
      cache_read_cost: component.cache_read_cost,
      total_cost: component.total_cost,
      actual_cost: component.actual_cost,
      unpriced_reason: component.unpriced_reason,
      ...(includeAdmin ? {
        account_stats_cost: component.account_stats_cost,
        pricing_source: component.pricing_source,
      } : {}),
    })),
  })
}

// Spreadsheet cells cannot hold an arbitrary number of billing components. Each
// component gets its own row, linked to the main usage row by usage/request ID.
export const BILLING_DETAIL_EXPORT_HEADERS = [
  'usage_id', 'request_id', 'created_at', 'billing_status', 'version',
  'role', 'iteration_index', 'model', 'billing_mode',
  'input_tokens', 'output_tokens', 'cache_creation_tokens', 'cache_read_tokens',
  'cache_creation_5m_tokens', 'cache_creation_1h_tokens',
  'cache_creation_30m_tokens', 'cache_creation_12h_tokens',
  'input_cost', 'output_cost', 'cache_creation_cost', 'cache_read_cost',
  'total_cost', 'actual_cost', 'unpriced_reason', 'account_stats_cost', 'pricing_source',
] as const

export function usageBillingDetailExportRows(
  log: Pick<AdminUsageLog, 'id' | 'request_id' | 'created_at' | 'billing_details'>,
  notPriced: string,
): (string | number)[][] {
  const details = log.billing_details
  if (!details) return []
  const cost = (value: number | null | undefined): string =>
    value == null ? notPriced : value.toFixed(8)
  return details.components.map(component => [
    log.id, log.request_id, log.created_at, details.status, details.version,
    component.role, component.iteration_index ?? '', component.model, component.billing_mode,
    component.input_tokens, component.output_tokens, component.cache_creation_tokens, component.cache_read_tokens,
    component.cache_creation_5m_tokens, component.cache_creation_1h_tokens,
    component.cache_creation_30m_tokens ?? 0, component.cache_creation_12h_tokens ?? 0,
    cost(component.input_cost), cost(component.output_cost), cost(component.cache_creation_cost), cost(component.cache_read_cost),
    cost(component.total_cost), cost(component.actual_cost), component.unpriced_reason ?? '',
    cost(component.account_stats_cost), component.pricing_source ?? '',
  ])
}
