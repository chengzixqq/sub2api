// Same bounds the backend enforces for ops_metrics_interval_seconds.
export const OPS_METRICS_INTERVAL_MIN_SECONDS = 60
export const OPS_METRICS_INTERVAL_MAX_SECONDS = 3600
const OPS_METRICS_INTERVAL_DEFAULT_SECONDS = 60

export function clampOpsMetricsIntervalSeconds(value: unknown): number {
  const seconds = Math.floor(Number(value))
  if (!Number.isFinite(seconds) || seconds <= 0) {
    return OPS_METRICS_INTERVAL_DEFAULT_SECONDS
  }
  return Math.min(OPS_METRICS_INTERVAL_MAX_SECONDS, Math.max(OPS_METRICS_INTERVAL_MIN_SECONDS, seconds))
}

/**
 * The GET /admin/settings value of ops_monitoring_enabled is "config switch && setting",
 * so writing it back unchanged would persist a false that only the config caused.
 * Send it only when the admin changed it and the config does not force ops off.
 */
export function shouldSendOpsMonitoringEnabled(
  current: boolean,
  loaded: boolean | null,
  configEnabled: boolean | null,
): boolean {
  return configEnabled !== false && loaded !== null && current !== loaded
}

export interface OpsMonitoringSettingsValues {
  ops_monitoring_enabled: boolean
  ops_realtime_monitoring_enabled: boolean
  ops_query_mode_default: string
  ops_metrics_interval_seconds: number
}

export function pickOpsMonitoringSettings(source: OpsMonitoringSettingsValues): OpsMonitoringSettingsValues {
  return {
    ops_monitoring_enabled: Boolean(source.ops_monitoring_enabled),
    ops_realtime_monitoring_enabled: Boolean(source.ops_realtime_monitoring_enabled),
    ops_query_mode_default: source.ops_query_mode_default || 'auto',
    ops_metrics_interval_seconds: source.ops_metrics_interval_seconds,
  }
}

/**
 * The ops fields for PUT /admin/settings: only the ones the admin changed since `loaded`.
 * Omitted fields keep their stored value, so an untouched save cannot revert a change
 * made elsewhere in the meantime (e.g. the App via ops/monitoring-status).
 */
export function buildOpsMonitoringSettingsUpdate(
  current: OpsMonitoringSettingsValues,
  loaded: OpsMonitoringSettingsValues | null,
  configEnabled: boolean | null,
): Partial<OpsMonitoringSettingsValues> {
  if (loaded === null) {
    return {}
  }
  const next = pickOpsMonitoringSettings(current)
  const base = pickOpsMonitoringSettings(loaded)
  const update: Partial<OpsMonitoringSettingsValues> = {}
  if (shouldSendOpsMonitoringEnabled(next.ops_monitoring_enabled, base.ops_monitoring_enabled, configEnabled)) {
    update.ops_monitoring_enabled = next.ops_monitoring_enabled
  }
  if (next.ops_realtime_monitoring_enabled !== base.ops_realtime_monitoring_enabled) {
    update.ops_realtime_monitoring_enabled = next.ops_realtime_monitoring_enabled
  }
  if (next.ops_query_mode_default !== base.ops_query_mode_default) {
    update.ops_query_mode_default = next.ops_query_mode_default
  }
  if (Number(next.ops_metrics_interval_seconds) !== Number(base.ops_metrics_interval_seconds)) {
    update.ops_metrics_interval_seconds = clampOpsMetricsIntervalSeconds(next.ops_metrics_interval_seconds)
  }
  return update
}
