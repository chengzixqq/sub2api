<template>
  <section class="space-y-4 border-b border-gray-200 pb-6 dark:border-dark-700">
    <header class="flex flex-wrap items-center justify-between gap-3"><h2 class="text-base font-semibold">{{ t('channelMonitorV2.unified.settings.title') }}</h2><button type="submit" :form="formId" class="btn btn-primary" :disabled="saving || loading || !draft || Boolean(conflict)"><Icon name="check" size="sm" />{{ t('common.save') }}</button></header>
    <p v-if="savedMessage" role="status" data-testid="settings-saved" class="rounded-xl border border-emerald-200 bg-emerald-50 px-3 py-2 text-sm text-emerald-800 dark:border-emerald-900 dark:bg-emerald-950/30 dark:text-emerald-200">{{ savedMessage }}</p>
    <p v-if="preview" class="rounded-xl bg-primary-50 p-3 text-xs text-primary-700 dark:bg-primary-950/30 dark:text-primary-300">{{ text('本地验收：配置只保存在本页内存中，不连接后台或执行付费探测。', 'Local preview: settings stay in memory; no backend requests or paid probes.') }}</p>
    <p v-if="loading" class="py-6 text-sm text-gray-500">{{ t('common.loading') }}</p>
    <div v-else-if="!draft" role="alert" class="flex items-center gap-3 text-sm text-amber-700"><span>{{ t('channelMonitorV2.unified.loadFailed') }}</span><button type="button" class="btn btn-secondary" @click="load">{{ t('common.retry') }}</button></div>
    <form v-else :id="formId" class="space-y-5" @submit.prevent="save">
      <p v-if="validation" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ validation }}</p>
      <div v-if="conflict" role="alert" class="space-y-2 rounded-xl border border-amber-200 bg-amber-50 p-3 text-sm text-amber-900 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-200"><p>{{ text('服务器配置已改变。你的草稿已保留；请记录修改后重新载入最新配置再合并。', 'The server configuration changed. Your draft is retained. Record your edits, reload the latest configuration, and merge them.') }}</p><button type="button" class="btn btn-secondary btn-sm" @click="load">{{ text('放弃草稿，载入最新配置', 'Discard draft and reload') }}</button></div>
      <fieldset :disabled="saving" class="space-y-5">
        <div class="flex flex-wrap items-center gap-x-8 gap-y-4">
          <label class="inline-flex items-center gap-2 text-sm"><input v-model="draft.enabled" data-testid="collection-enabled" type="checkbox" class="checkbox" />{{ t('channelMonitorV2.unified.settings.enabled') }}</label>
          <label class="inline-flex items-center gap-2 text-sm"><input v-model="draft.probe_enabled" data-testid="probe-enabled" type="checkbox" class="checkbox" />{{ t('channelMonitorV2.unified.settings.probeEnabled') }}</label>
          <label class="inline-flex items-center gap-2 text-sm"><input v-model="draft.quota_enabled" data-testid="quota-enabled" type="checkbox" class="checkbox" />{{ t('channelMonitorV2.unified.settings.quotaEnabled') }}</label>
          <a v-if="!preview" href="/admin/settings" class="text-sm text-primary-600 hover:underline dark:text-primary-400">{{ t(displayEnabled ? 'channelMonitorV2.unified.settings.displayOn' : 'channelMonitorV2.unified.settings.displayOff') }}</a>
        </div>
        <div class="grid gap-4 sm:grid-cols-3">
          <div><label :for="formId + '-mode'" class="input-label">{{ t('channelMonitorV2.unified.settings.mode') }}</label><Select :dropdown-class="motionClass" :id="formId + '-mode'" v-model="draft.mode" data-testid="publication" :options="modeOptions" :searchable="false" :disabled="saving" /></div>
          <label class="block"><span class="input-label">{{ text('页面刷新间隔（秒，30–300）', 'Page refresh interval (seconds, 30–300)') }}</span><input v-model.number="draft.refresh_interval_seconds" data-testid="refresh-interval" class="input" type="number" min="30" max="300" step="1" required /></label>
          <div class="text-xs text-gray-500"><p class="input-label">{{ t('channelMonitorV2.unified.settings.retention') }}</p><p class="pt-2">{{ text('明细固定保留24小时；聚合历史不受此项影响。', 'Detailed samples are retained for 24 hours; aggregated history is separate.') }}</p></div>
        </div>
        <fieldset v-if="draft.mode === 'live'" class="space-y-2"><legend class="mb-2 text-sm font-medium">{{ t('channelMonitorV2.unified.settings.liveGroups') }} <span v-if="!draft.live_group_ids?.length" class="badge badge-warning">{{ t('channelMonitorV2.unified.settings.allGroups') }}</span></legend><div class="flex max-h-48 flex-wrap gap-x-5 gap-y-3 overflow-y-auto"><label v-for="group in groups" :key="group.id" class="inline-flex items-center gap-2 text-sm"><input :data-testid="`live-group-${group.id}`" type="checkbox" class="checkbox" :checked="draft.live_group_ids?.includes(group.id)" @change="toggleGroup(group.id)" /><span>{{ group.name }}</span></label></div></fieldset>
        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
          <label class="block"><span class="input-label">{{ t('channelMonitorV2.unified.settings.minimumSample') }}</span><input v-model.number="draft.minimum_sample" data-testid="minimum-sample" class="input" type="number" min="1" max="1000000" step="1" required /></label>
          <label class="block"><span class="input-label">{{ t('channelMonitorV2.unified.settings.healthyReliability') }}</span><input v-model.number="healthyPercent" data-testid="healthy-reliability" class="input" type="number" min="0.01" max="100" step="0.01" required /></label>
          <label class="block"><span class="input-label">{{ t('channelMonitorV2.unified.settings.warningReliability') }}</span><input v-model.number="warningPercent" data-testid="warning-reliability" class="input" type="number" min="0.01" max="100" step="0.01" required /></label>
          <label class="block"><span class="input-label">{{ t('channelMonitorV2.unified.settings.warningTtft') }}</span><input v-model.number="draft.warning_ttft_ms" class="input" type="number" min="1" max="86400000" step="1" required /></label>
          <label class="block"><span class="input-label">{{ t('channelMonitorV2.unified.settings.criticalTtft') }}</span><input v-model.number="draft.critical_ttft_ms" class="input" type="number" min="1" max="86400000" step="1" required /></label>
        </div>
        <fieldset class="space-y-3 rounded-xl border border-gray-200 p-4 dark:border-dark-700"><legend class="px-1 text-sm font-medium">{{ text('证据时效与故障确认', 'Evidence freshness and failure confirmation') }}</legend><p class="text-xs text-gray-500">{{ text('真实流量超时后回退到探测证据；探测连续失败达到阈值才确认故障。额度同步固定每5分钟一次，超过时效标记陈旧。', 'Expired traffic falls back to probe evidence; repeated probe failures confirm an outage. Quota sync runs every five minutes and becomes stale after its configured age.') }}</p><div class="grid gap-4 sm:grid-cols-3"><label v-for="field in freshnessFields" :key="field.key" class="block"><span class="input-label">{{ field.label }}</span><input v-model.number="draft[field.key]" :data-testid="field.key" class="input" type="number" :min="field.min" :max="field.max" step="1" required /></label></div></fieldset>
        <fieldset class="space-y-3 rounded-xl border border-gray-200 p-4 dark:border-dark-700"><legend class="px-1 text-sm font-medium">{{ text('探测调度与预算', 'Probe scheduling and budgets') }}</legend><p class="text-xs text-gray-500">{{ text('探测运行需要真实采集、付费探活和目标开关均开启；自动探测还需采集正常且近期无流量。预算按UTC日重置，单目标与全局限额同时生效；可能产生上游费用。', 'Probe execution requires collection, paid probes and the target enabled; automatic probes also require healthy collection and idle traffic. Daily UTC target and global limits both apply; upstream usage may be billed.') }}</p><div class="grid gap-4 sm:grid-cols-2">
          <label v-for="field in probeFields" :key="field.key" class="block"><span class="input-label">{{ field.label }}</span><input v-model.number="draft[field.key]" :data-testid="field.key" class="input" type="number" :min="field.min" :max="field.max" step="1" required /></label>
        </div></fieldset>
        <fieldset class="space-y-3 border-t border-gray-200 pt-4 dark:border-dark-700"><legend class="text-sm font-medium">{{ text('首字延迟覆盖规则', 'First-output latency overrides') }}</legend><p class="text-xs leading-5 text-gray-500">{{ text('选择器精确匹配，留空表示不限；至少设置一个。更具体的组合优先：平台+分组+模型 > 分组+模型 > 平台+模型 > 平台+分组 > 模型 > 分组 > 平台。未命中时使用全局阈值。', 'Selectors match exactly; blank means any. Priority: platform+group+model > group+model > platform+model > platform+group > model > group > platform. Otherwise global thresholds apply.') }}</p>
          <div v-for="(rule, index) in draft.overrides" :key="index" class="space-y-3 rounded-xl border border-gray-200 p-3 dark:border-dark-700" :data-testid="`override-${index}`">
            <div class="flex items-center justify-between"><span class="text-xs font-medium">{{ text('规则', 'Rule') }} {{ index + 1 }}</span><button type="button" class="btn btn-ghost btn-sm" @click="draft.overrides.splice(index, 1)">{{ t('common.delete') }}</button></div>
            <div class="grid gap-3 sm:grid-cols-3"><div><label class="input-label">{{ text('平台', 'Platform') }}</label><Select :dropdown-class="motionClass" v-model="rule.platform" :options="platformOptions" :disabled="saving" :searchable="false" /></div><div><label class="input-label">{{ text('分组', 'Group') }}</label><Select :dropdown-class="motionClass" v-model="rule.group_id" :options="groupOptions" :disabled="saving" /></div><label><span class="input-label">{{ text('模型（精确名称）', 'Model (exact name)') }}</span><input v-model.trim="rule.model" class="input" maxlength="192" /></label></div>
            <div class="grid gap-3 sm:grid-cols-2"><label><span class="input-label">{{ t('channelMonitorV2.unified.settings.warningTtft') }}</span><input v-model.number="rule.warning_ttft_ms" class="input" type="number" min="1" max="86400000" step="1" required /></label><label><span class="input-label">{{ t('channelMonitorV2.unified.settings.criticalTtft') }}</span><input v-model.number="rule.critical_ttft_ms" class="input" type="number" min="1" max="86400000" step="1" required /></label></div>
          </div>
          <button type="button" data-testid="add-override" class="btn btn-secondary btn-sm" :disabled="draft.overrides.length >= 100" @click="addOverride"><Icon name="plus" size="sm" />{{ text('添加规则', 'Add rule') }}</button>
        </fieldset>
      </fieldset>
    </form>
  </section>
</template>
<script setup lang="ts">
import { computed, getCurrentInstance, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import Select from '@/components/common/Select.vue'
import { useMonitorMotion } from './useMonitorPreferences'
import { adminAPI } from '@/api/admin'
import { getObservationConfig, updateObservationConfig, type ObservationConfig } from '@/api/channelMonitorV2'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import { isChannelMonitorRouteEnabled } from '@/utils/featureFlags'
import { cloneSettings, monitorSettingsPreview, previewGroups } from './monitorSettingsPreview'
import { finite, observationPolicyDefaults, validateObservation } from './monitorSettingsValidation'
const motionClass = useMonitorMotion()
const props = withDefaults(defineProps<{ preview?: boolean }>(), { preview: false })
const emit = defineEmits<{ saved: [] }>()
const { t, locale } = useI18n()
const text = (zh: string, en: string) => (locale?.value || 'zh').startsWith('zh') ? zh : en
const app = useAppStore()
const formId = `observation-policy-${getCurrentInstance()?.uid}`
const loading = ref(true)
const saving = ref(false)
const conflict = ref(false)
const validation = ref('')
const savedMessage = ref('')
const draft = ref<ObservationConfig | null>(null)
const groups = ref<Array<{ id: number; name: string }>>([])
const displayEnabled = computed(isChannelMonitorRouteEnabled)
const modeOptions = computed(() => [{ value: 'shadow', label: t('channelMonitorV2.unified.settings.shadow') }, { value: 'live', label: t('channelMonitorV2.unified.settings.live') }])
const platformOptions = computed(() => [{ value: '', label: text('不限平台', 'Any platform') }, ...[...new Set(['anthropic', 'openai', 'gemini', 'antigravity', ...(draft.value?.overrides.map(r => r.platform).filter(Boolean) as string[] || [])])].map(value => ({ value, label: value }))])
const groupOptions = computed(() => [{ value: 0, label: text('不限分组', 'Any group') }, ...groups.value.map(g => ({ value: g.id, label: g.name })), ...(draft.value?.overrides.filter(r => r.group_id && !groups.value.some(g => g.id === r.group_id)).map(r => ({ value: r.group_id!, label: `#${r.group_id}` })) || [])])
const freshnessFields = computed(() => [
  { key: 'traffic_freshness_seconds' as const, label: text('流量证据有效期（秒，60–3600）', 'Traffic freshness (seconds, 60–3600)'), min: 60, max: 3600 },
  { key: 'probe_failure_threshold' as const, label: text('探测连续失败阈值（1–10）', 'Consecutive probe failures (1–10)'), min: 1, max: 10 },
  { key: 'quota_stale_seconds' as const, label: text('额度证据过期（秒，300–3600）', 'Quota stale age (seconds, 300–3600)'), min: 300, max: 3600 },
])
const probeFields = computed(() => [
  { key: 'probe_interval_seconds' as const, label: text('探测间隔（秒，60–3600）', 'Probe interval (seconds, 60–3600)'), min: 60, max: 3600 },
  { key: 'probe_timeout_seconds' as const, label: text('探测超时（秒，5–60）', 'Probe timeout (seconds, 5–60)'), min: 5, max: 60 },
  { key: 'probe_target_daily_limit' as const, label: text('单目标每日最多次数（1–1440）', 'Daily target limit (1–1440)'), min: 1, max: 1440 },
  { key: 'probe_global_daily_limit' as const, label: text('全局每日最多次数（1–100000）', 'Daily global limit (1–100000)'), min: 1, max: 100000 },
])
function percentage(key: 'healthy_reliability' | 'warning_reliability') { return computed({ get: () => finite(draft.value?.[key]) ? draft.value![key] * 100 : '', set: (value: number | string) => { if (draft.value) draft.value[key] = value === '' ? Number.NaN : Number(value) / 100 } }) }
const healthyPercent = percentage('healthy_reliability')
const warningPercent = percentage('warning_reliability')
let epoch = 0
onBeforeUnmount(() => { epoch++ })
async function load() {
  if (saving.value) return
  const request = ++epoch
  loading.value = true
  try {
    const [policy, available] = props.preview ? [monitorSettingsPreview.getObservation(), previewGroups] : await Promise.all([getObservationConfig(), adminAPI.groups.getAll()])
    if (request !== epoch) return
    draft.value = cloneSettings({ ...observationPolicyDefaults, ...policy, probe_enabled: policy.probe_enabled ?? false, quota_enabled: policy.quota_enabled ?? true, live_group_ids: policy.live_group_ids || [], overrides: policy.overrides || [] })
    groups.value = available
    conflict.value = false
    validation.value = ''
  } catch (error) { if (request === epoch) app.showError(extractApiErrorMessage(error, t('channelMonitorV2.unified.loadFailed'))) }
  finally { if (request === epoch) loading.value = false }
}
function toggleGroup(id: number) {
  if (!draft.value) return
  const selected = new Set(draft.value.live_group_ids)
  if (selected.has(id)) selected.delete(id)
  else selected.add(id)
  draft.value.live_group_ids = [...selected].sort((a, b) => a - b)
}
function addOverride() { if (draft.value) draft.value.overrides.push({ platform: '', group_id: 0, model: '', warning_ttft_ms: draft.value.warning_ttft_ms, critical_ttft_ms: draft.value.critical_ttft_ms }) }
async function save() {
  if (!draft.value || saving.value || loading.value || conflict.value) return
  const problem = validateObservation(draft.value)
  const errors: Record<string, string> = {
    reliability: text('可靠率必须满足：0 < 警告线 < 正常线 ≤ 100%，不可留空。', 'Reliability must satisfy 0 < warning < healthy ≤ 100%; blanks are not allowed.'),
    latency: text('首字延迟必须为整数，0 < 警告线 < 严重线 ≤ 86400000毫秒。', 'Latency must be integer milliseconds: 0 < warning < critical ≤ 86400000.'),
    overrides: text('请检查覆盖规则：至少一个选择器、有效名称与分组，以及递增的整数延迟阈值；最多100条。', 'Check override selectors, valid names/groups and increasing integer latency thresholds; maximum 100 rules.'),
    duplicate: text('覆盖规则的选择器组合重复，请合并。', 'Duplicate override selectors; combine the rules.'),
  }
  if (problem) { validation.value = errors[problem] || text('参数为空或超出允许范围，请检查表单标注。', 'A parameter is blank or outside its stated range.'); return }
  validation.value = ''
  savedMessage.value = ''
  saving.value = true
  const request = epoch
  try {
    const payload = cloneSettings(draft.value)
    const result = props.preview ? monitorSettingsPreview.saveObservation(payload) : await updateObservationConfig(payload)
    if (request !== epoch) return
    draft.value = cloneSettings(result)
    emit('saved')
    savedMessage.value = props.preview ? text('上次配置保存成功（本地预览，不影响线上）。', 'Last settings save succeeded locally; production is unchanged.') : t('channelMonitorV2.unified.settings.saved')
    app.showSuccess(props.preview ? text('已保存本地预览配置', 'Local preview settings saved') : t('channelMonitorV2.unified.settings.saved'))
  } catch (error) {
    if (request !== epoch) return
    app.showError(extractApiErrorMessage(error, t('channelMonitorV2.settings.saveFailed')))
    conflict.value = (error as { response?: { status?: number } }).response?.status === 409
  } finally { if (request === epoch) saving.value = false }
}
onMounted(load)
</script>
