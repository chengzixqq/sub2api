import { describe, expect, it } from 'vitest'

import {
  buildOpsMonitoringSettingsUpdate,
  clampOpsMetricsIntervalSeconds,
  shouldSendOpsMonitoringEnabled,
  type OpsMonitoringSettingsValues,
} from '../opsMonitoringSettings'

describe('opsMonitoringSettings helpers', () => {
  it('sends the master switch only when changed and the config allows it', () => {
    expect(shouldSendOpsMonitoringEnabled(true, false, true)).toBe(true)
    expect(shouldSendOpsMonitoringEnabled(true, false, null)).toBe(true)
    expect(shouldSendOpsMonitoringEnabled(false, false, true)).toBe(false)
    // GET merged the config switch into false; never write that back.
    expect(shouldSendOpsMonitoringEnabled(true, false, false)).toBe(false)
    expect(shouldSendOpsMonitoringEnabled(true, null, true)).toBe(false)
  })

  const loaded: OpsMonitoringSettingsValues = {
    ops_monitoring_enabled: true,
    ops_realtime_monitoring_enabled: true,
    ops_query_mode_default: 'auto',
    ops_metrics_interval_seconds: 120,
  }

  it('builds an empty update when nothing changed or nothing was loaded', () => {
    expect(buildOpsMonitoringSettingsUpdate({ ...loaded }, loaded, true)).toEqual({})
    expect(buildOpsMonitoringSettingsUpdate({ ...loaded, ops_realtime_monitoring_enabled: false }, null, true)).toEqual({})
  })

  it('includes only the changed ops settings', () => {
    expect(buildOpsMonitoringSettingsUpdate({ ...loaded, ops_realtime_monitoring_enabled: false }, loaded, true)).toEqual({
      ops_realtime_monitoring_enabled: false,
    })
    expect(buildOpsMonitoringSettingsUpdate({ ...loaded, ops_query_mode_default: 'raw' }, loaded, true)).toEqual({
      ops_query_mode_default: 'raw',
    })
    expect(buildOpsMonitoringSettingsUpdate({ ...loaded, ops_metrics_interval_seconds: 5000 }, loaded, true)).toEqual({
      ops_metrics_interval_seconds: 3600,
    })
    expect(buildOpsMonitoringSettingsUpdate({ ...loaded, ops_monitoring_enabled: false }, loaded, true)).toEqual({
      ops_monitoring_enabled: false,
    })
    // The master switch keeps its config guard; the sub-settings still save.
    expect(
      buildOpsMonitoringSettingsUpdate(
        { ...loaded, ops_monitoring_enabled: false, ops_query_mode_default: 'raw' },
        loaded,
        false,
      ),
    ).toEqual({ ops_query_mode_default: 'raw' })
  })

  it('clamps the metrics interval to the backend range', () => {
    expect(clampOpsMetricsIntervalSeconds(30)).toBe(60)
    expect(clampOpsMetricsIntervalSeconds(5000)).toBe(3600)
    expect(clampOpsMetricsIntervalSeconds('120')).toBe(120)
    expect(clampOpsMetricsIntervalSeconds('')).toBe(60)
    expect(clampOpsMetricsIntervalSeconds(Number.NaN)).toBe(60)
  })
})
