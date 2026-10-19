import { describe, expect, it } from 'vitest'

import { clampOpsMetricsIntervalSeconds, shouldSendOpsMonitoringEnabled } from '../opsMonitoringSettings'

describe('opsMonitoringSettings helpers', () => {
  it('sends the master switch only when changed and the config allows it', () => {
    expect(shouldSendOpsMonitoringEnabled(true, false, true)).toBe(true)
    expect(shouldSendOpsMonitoringEnabled(true, false, null)).toBe(true)
    expect(shouldSendOpsMonitoringEnabled(false, false, true)).toBe(false)
    // GET merged the config switch into false; never write that back.
    expect(shouldSendOpsMonitoringEnabled(true, false, false)).toBe(false)
    expect(shouldSendOpsMonitoringEnabled(true, null, true)).toBe(false)
  })

  it('clamps the metrics interval to the backend range', () => {
    expect(clampOpsMetricsIntervalSeconds(30)).toBe(60)
    expect(clampOpsMetricsIntervalSeconds(5000)).toBe(3600)
    expect(clampOpsMetricsIntervalSeconds('120')).toBe(120)
    expect(clampOpsMetricsIntervalSeconds('')).toBe(60)
    expect(clampOpsMetricsIntervalSeconds(Number.NaN)).toBe(60)
  })
})
