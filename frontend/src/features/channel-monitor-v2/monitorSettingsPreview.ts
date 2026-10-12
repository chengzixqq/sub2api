import type { MonitorConfig, MonitorProbeBudget, MonitorProbeTarget, ObservationConfig } from '@/api/channelMonitorV2'
import { observationPolicyDefaults } from './monitorSettingsValidation'
export const cloneSettings = <T>(value: T): T => JSON.parse(JSON.stringify(value))
export const previewGroups = [{ id: 1, name: 'Claude 标准渠道 · 预览', platform: 'anthropic' }, { id: 2, name: 'ChatGPT 标准渠道 · 预览', platform: 'openai' }]
let observation: ObservationConfig = { version: 1, enabled: true, probe_enabled: false, quota_enabled: true, mode: 'shadow', live_group_ids: [], detail_retention_hours: 24, refresh_interval_seconds: 60, minimum_sample: 50, healthy_reliability: .99, warning_reliability: .95, warning_ttft_ms: 3000, critical_ttft_ms: 10000, overrides: [], ...observationPolicyDefaults }
let legacy: MonitorConfig = { version: 1, enabled: true, refresh_interval_seconds: 60, platforms: [{ platform: 'anthropic', enabled: true, models: [] }, { platform: 'openai', enabled: true, models: [] }, { platform: 'gemini', enabled: false, models: [] }, { platform: 'antigravity', enabled: false, models: [] }], group_ids: [], ignored_error_categories: [], health_thresholds: { minimum_sample: 50, warning_error_rate: .05, critical_error_rate: .2, target_ttft_ms: 3000, warning_ttft_ms: 3000, critical_ttft_ms: 10000, warning_cache_rate: 0, critical_cache_rate: 0, error_weight: .6, ttft_weight: .2, cache_weight: .2 } }
let targets: MonitorProbeTarget[] = [{ id: 1, group_id: 2, model: 'preview-model', protocol: 'openai_responses', enabled: false, version: 1, created_at: '2026-10-12T00:00:00Z', updated_at: '2026-10-12T00:00:00Z' }]
export const monitorSettingsPreview = {
  getObservation: () => cloneSettings(observation),
  saveObservation: (value: ObservationConfig) => { observation = cloneSettings({ ...value, version: (observation.version || 0) + 1 }); return cloneSettings(observation) },
  getLegacy: () => cloneSettings(legacy),
  saveLegacy: (value: MonitorConfig) => { legacy = cloneSettings({ ...value, version: legacy.version + 1 }); return cloneSettings(legacy) },
  getTargets: () => cloneSettings(targets),
  saveTarget: (value: Partial<MonitorProbeTarget> & Pick<MonitorProbeTarget, 'group_id' | 'model' | 'protocol' | 'enabled'>) => {
    const row = { ...value, id: value.id || Math.max(0, ...targets.map(t => t.id)) + 1, version: (value.version || 0) + 1, created_at: value.created_at || new Date().toISOString(), updated_at: new Date().toISOString() } as MonitorProbeTarget
    targets = [...targets.filter(t => t.id !== row.id), cloneSettings(row)]
    return cloneSettings(row)
  },
  getBudget: (): MonitorProbeBudget => ({ utc_day: new Date().toISOString().slice(0, 10), global_limit: observation.probe_global_daily_limit || 1000, global_used: 0, targets: targets.map(t => ({ target_id: t.id, limit: observation.probe_target_daily_limit || 96, used: 0 })) }),
}
