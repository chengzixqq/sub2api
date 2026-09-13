import { describe, expect, it } from 'vitest'
import { createUsageRequests, last24Hours, localMinute, reconcileUsageTotal, snapshotUsageQuery, usageGranularity } from '../usageQuery'

describe('usage query snapshots', () => {
  it('keeps an exact 24 hour interval at minute precision', () => {
    const range = last24Hours(new Date(2026, 8, 14, 10, 31, 59))
    expect(new Date(range.end).getTime() - new Date(range.start).getTime()).toBe(86_400_000)
    expect(localMinute(new Date(range.end))).toMatch(/T10:31$/)
    expect(usageGranularity(range.start, range.end)).toBe('hour')
  })
  it('preserves all filters and explicit offsets without extending the end', () => {
    const filters = { model: 'a', billing_mode: 'token', start_date: '2026-09-13' }
    const query = snapshotUsageQuery(filters, '2026-09-14T10:01:00+08:00', '2026-09-14T10:02:00+08:00')
    filters.model = 'b'
    expect(query).toMatchObject({ model: 'a', billing_mode: 'token', start_time: '2026-09-14T02:01:00.000Z', end_time: '2026-09-14T02:02:00.000Z', start_date: undefined, end_date: undefined })
    expect(Object.isFrozen(query)).toBe(true)
  })
  it('rejects invalid and reversed intervals', () => {
    expect(() => snapshotUsageQuery({}, 'invalid', '')).toThrow(RangeError)
    expect(() => snapshotUsageQuery({}, '2026-09-14T10:00', '2026-09-14T09:00')).toThrow(RangeError)
  })
  it('keeps explicit DST offsets unambiguous', () => {
    const query = snapshotUsageQuery({}, '2026-11-01T01:30:00-04:00', '2026-11-01T01:30:00-05:00')
    expect(new Date(query.end_time!).getTime() - new Date(query.start_time!).getTime()).toBe(3_600_000)
  })
  it('preserves the real last 24 hours even inside the repeated DST hour', () => {
    const range = last24Hours(new Date('2026-11-01T01:30:59-05:00'))
    expect(range.end).toBe('2026-11-01T06:30:00.000Z')
    expect(range.start).toBe('2026-10-31T06:30:00.000Z')
  })
  it('does not present a stale cached count as an exact pagination total', () => {
    expect(reconcileUsageTotal(20, 1, 20, 20, true)).toBeNull()
    expect(reconcileUsageTotal(20, 2, 20, 4, false)).toBeNull()
    expect(reconcileUsageTotal(24, 2, 20, 4, false)).toBe(24)
    expect(reconcileUsageTotal(1, 1, 20, 0, false)).toBeNull()
    expect(reconcileUsageTotal(10, 2, 20, 0, false)).toBe(10)
  })
  it('cancels only superseded regions, and invalidates every region on query change', () => {
    const requests = createUsageRequests()
    const oldStats = requests.start('stats')
    const logs = requests.start('logs')
    const stats = requests.start('stats')
    expect(oldStats.signal.aborted).toBe(true)
    expect(oldStats.current()).toBe(false)
    expect(logs.current()).toBe(true)
    expect(stats.current()).toBe(true)
    requests.cancelAll()
    expect(logs.current()).toBe(false)
    expect(stats.signal.aborted).toBe(true)
  })
})
