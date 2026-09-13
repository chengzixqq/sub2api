import type { UsageQueryParams, PaginatedResponse } from '@/types'
import { requestTypeToLegacyStream } from '@/utils/usageRequestType'

export interface UsageQueryMetadata {
  start_time: string
  end_time: string
  timezone: string
  generated_at: string
}

export interface UsagePage<T> extends Omit<PaginatedResponse<T>, 'total' | 'pages'> {
  total: number | null
  pages: number | null
  total_exact?: boolean
  has_more?: boolean
  query?: UsageQueryMetadata
}

export function localMinute(date: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`
}

export function last24Hours(now = new Date()): { start: string; end: string } {
  const end = new Date(Math.floor(now.getTime() / 60_000) * 60_000)
  return { start: new Date(end.getTime() - 86_400_000).toISOString(), end: end.toISOString() }
}

export function reconcileUsageTotal(total: number | null | undefined, page: number, pageSize: number, itemCount: number, hasMore: boolean): number | null {
  if (total == null) return null
  const observed = (page - 1) * pageSize + itemCount
  if ((itemCount > 0 && total < observed) || (hasMore && total <= page * pageSize) || (!hasMore && itemCount > 0 && total !== observed) || (!hasMore && itemCount === 0 && total > observed)) return null
  return total
}

export function usageGranularity(start: string, end: string): 'hour' | 'day' {
  return new Date(end).getTime() - new Date(start).getTime() <= 86_400_000 ? 'hour' : 'day'
}

// Snapshot scalar filters once so exports and concurrent regions share identical boundaries.
export function snapshotUsageQuery<T extends UsageQueryParams>(filters: T, start: string, end: string): Readonly<T & UsageQueryParams> {
  const from = new Date(start)
  const to = new Date(end)
  if (!Number.isFinite(from.getTime()) || !Number.isFinite(to.getTime()) || from >= to) {
    throw new RangeError('Invalid usage time range')
  }
  const legacyStream = filters.request_type ? requestTypeToLegacyStream(filters.request_type) : filters.stream
  return Object.freeze({
    ...filters,
    start_date: undefined,
    end_date: undefined,
    start_time: from.toISOString(),
    end_time: to.toISOString(),
    timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
    stream: legacyStream === null ? undefined : legacyStream,
  })
}

// Cancellation plus identity checks also reject late completions from non-abortable mocks/transports.
export function createUsageRequests() {
  const controllers = new Map<string, AbortController>()
  return {
    start(region: string) {
      controllers.get(region)?.abort()
      const controller = new AbortController()
      controllers.set(region, controller)
      return { signal: controller.signal, current: () => controllers.get(region) === controller && !controller.signal.aborted }
    },
    cancelAll() {
      for (const controller of controllers.values()) controller.abort()
      controllers.clear()
    },
  }
}
