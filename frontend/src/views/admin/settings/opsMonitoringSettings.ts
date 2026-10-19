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
