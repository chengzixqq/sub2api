import { describe, expect, it } from 'vitest'
import { monitorSettingsPreview } from '../monitorSettingsPreview'
import { validateObservation, validateLegacy } from '../monitorSettingsValidation'

describe('monitor settings validation contracts', () => {
  it.each([
    ['refresh_interval_seconds', 0], ['refresh_interval_seconds', 29], ['refresh_interval_seconds', 301], ['refresh_interval_seconds', 30.5],
    ['probe_interval_seconds', 59], ['probe_interval_seconds', 3601], ['probe_timeout_seconds', 4], ['probe_timeout_seconds', 61],
    ['probe_target_daily_limit', 0], ['probe_target_daily_limit', 1441], ['probe_global_daily_limit', 0], ['probe_global_daily_limit', 100001],
    ['traffic_freshness_seconds', 59], ['probe_failure_threshold', 0], ['probe_failure_threshold', 11], ['quota_stale_seconds', 299],
    ['minimum_sample', 0], ['minimum_sample', 1.5], ['healthy_reliability', Number.NaN], ['warning_reliability', 0],
  ])('rejects invalid %s=%s before a write', (key, value) => {
    expect(validateObservation({ ...monitorSettingsPreview.getObservation(), [key]: value })).not.toBeNull()
  })
  it('allows the exact documented bounds and a global cap lower than a per-target cap', () => {
    expect(validateObservation({ ...monitorSettingsPreview.getObservation(), refresh_interval_seconds: 30, probe_interval_seconds: 60, probe_timeout_seconds: 5, probe_target_daily_limit: 1440, probe_global_daily_limit: 1, traffic_freshness_seconds: 60, probe_failure_threshold: 10, quota_stale_seconds: 300 })).toBeNull()
    expect(validateObservation({ ...monitorSettingsPreview.getObservation(), refresh_interval_seconds: 300, probe_interval_seconds: 3600, probe_timeout_seconds: 60, probe_target_daily_limit: 1, probe_global_daily_limit: 100000, traffic_freshness_seconds: 3600, probe_failure_threshold: 1, quota_stale_seconds: 3600 })).toBeNull()
  })
  it('rejects duplicate or unconstrained overrides but permits distinct exact selectors', () => {
    const policy = monitorSettingsPreview.getObservation()
    const rule = { platform: 'openai', group_id: 7, model: 'gpt-test', warning_ttft_ms: 1, critical_ttft_ms: 2 }
    expect(validateObservation({ ...policy, overrides: [rule, { ...rule }] })).toBe('duplicate')
    expect(validateObservation({ ...policy, overrides: [{ warning_ttft_ms: 1, critical_ttft_ms: 2 }] })).toBe('overrides')
    expect(validateObservation({ ...policy, overrides: [rule, { ...rule, group_id: 8 }] })).toBeNull()
    expect(validateObservation({ ...policy, overrides: [{ ...rule, critical_ttft_ms: 1 }] })).toBe('overrides')
  })
  it('preserves valid zero cache settings and zero individual weights in the legacy policy', () => {
    const config = monitorSettingsPreview.getLegacy()
    expect(validateLegacy({ ...config, health_thresholds: { ...config.health_thresholds, warning_cache_rate: 0, critical_cache_rate: 0, error_weight: 1, ttft_weight: 0, cache_weight: 0 } })).toBeNull()
    expect(validateLegacy({ ...config, health_thresholds: { ...config.health_thresholds, error_weight: Number.NaN } })).toBe('weights')
    expect(validateLegacy({ ...config, health_thresholds: { ...config.health_thresholds, warning_error_rate: 0 } })).toBe('errors')
  })
})
