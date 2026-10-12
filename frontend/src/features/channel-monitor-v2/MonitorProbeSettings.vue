<template>
  <section class="space-y-4 border-b border-gray-200 pb-6 dark:border-dark-700">
    <header class="flex flex-wrap items-center justify-between gap-3">
      <div><h2 class="text-base font-semibold">{{ t('channelMonitorV2.unified.probes.title') }}</h2><p v-if="budget" class="mt-1 text-xs text-gray-500">{{ t('channelMonitorV2.unified.probes.budget') }} · {{ budget.utc_day }} · <span class="tabular-nums" :class="globalExhausted ? 'text-red-600' : ''">{{ t('channelMonitorV2.unified.probes.globalUsed', { used: budget.global_used, limit: budget.global_limit }) }}</span></p></div>
      <div class="flex items-center gap-2"><button type="button" class="btn btn-secondary btn-icon h-9 w-9" :disabled="loading || Boolean(busy)" :aria-label="t('common.refresh')" :title="t('common.refresh')" @click="load"><Icon name="refresh" size="sm" /></button><button data-testid="add-target" type="button" class="btn btn-primary" :disabled="loading || Boolean(busy)" @click="edit()"><Icon name="plus" size="sm" />{{ t('channelMonitorV2.unified.probes.add') }}</button></div>
    </header>
    <p v-if="savedMessage" role="status" data-testid="settings-saved" class="rounded-xl border border-emerald-200 bg-emerald-50 px-3 py-2 text-sm text-emerald-800 dark:border-emerald-900 dark:bg-emerald-950/30 dark:text-emerald-200">{{ savedMessage }}</p>
    <p v-if="preview" class="rounded-xl bg-primary-50 p-3 text-xs text-primary-700 dark:bg-primary-950/30 dark:text-primary-300">{{ text('本地验收：可编辑并保存模拟目标；探测运行已禁用，不产生请求或费用。', 'Local preview: edit targets in memory; probe execution is disabled and produces no requests or charges.') }}</p>
    <p v-if="loading" class="py-5 text-sm text-gray-500">{{ t('common.loading') }}</p>
    <p v-else-if="failed" role="alert" class="text-sm text-amber-700">{{ t('channelMonitorV2.unified.loadFailed') }}</p>
    <p v-else-if="!targets.length" class="py-5 text-sm text-gray-500">{{ t('channelMonitorV2.unified.probes.empty') }}</p>
    <div v-else class="overflow-x-auto">
      <table class="w-full text-left text-sm"><thead class="border-b border-gray-200 text-xs text-gray-500 dark:border-dark-700"><tr><th class="py-2">{{ t('channelMonitorV2.unified.probes.group') }}</th><th class="px-3 py-2">{{ t('channelMonitorV2.unified.probes.model') }}</th><th class="px-3 py-2">{{ t('channelMonitorV2.unified.probes.protocol') }}</th><th class="px-3 py-2">{{ t('channelMonitorV2.unified.probes.enabled') }}</th><th class="px-3 py-2">{{ t('channelMonitorV2.unified.probes.targetBudget') }}</th><th class="px-3 py-2">{{ t('channelMonitorV2.unified.probes.status') }}</th><th class="py-2 text-right">{{ t('common.actions') }}</th></tr></thead>
        <tbody><tr v-for="target in targets" :key="target.id" class="border-b border-gray-100 dark:border-dark-700">
          <td class="py-3">{{ groupName(target.group_id) }}</td><td class="max-w-64 break-all px-3 py-3 font-medium">{{ target.model }}</td><td class="px-3 py-3 text-xs text-gray-500">{{ protocolName(target.protocol) }}</td>
          <td class="px-3 py-3"><input type="checkbox" class="checkbox" :checked="target.enabled" :disabled="Boolean(busy)" :aria-label="t('channelMonitorV2.unified.probes.enabled')" @change="toggle(target)" /></td>
          <td class="px-3 py-3 tabular-nums">{{ targetBudget(target.id) }}</td>
          <td class="px-3 py-3"><span v-if="runs[target.id]" class="badge" :class="runs[target.id]?.status === 'healthy' ? 'badge-success' : 'badge-warning'">{{ t(`channelMonitorV2.unified.states.${runs[target.id]?.status}`) }}</span><span v-else class="text-gray-400">-</span></td>
          <td class="py-3"><div class="flex justify-end gap-2"><button type="button" class="btn btn-secondary btn-icon h-8 w-8" :disabled="Boolean(busy)" :aria-label="t('channelMonitorV2.unified.probes.edit')" :title="t('channelMonitorV2.unified.probes.edit')" @click="edit(target)"><Icon name="edit" size="sm" /></button><button :data-testid="`probe-${target.id}`" type="button" class="btn btn-secondary btn-icon h-8 w-8" :disabled="preview || Boolean(busy) || exhausted(target.id)" :aria-label="t('channelMonitorV2.unified.probes.run')" :title="t(exhausted(target.id) ? 'channelMonitorV2.unified.probes.budgetExhausted' : 'channelMonitorV2.unified.probes.run')" @click="confirmTarget = target"><Icon name="play" size="sm" /></button></div></td>
        </tr></tbody>
      </table>
    </div>
    <BaseDialog :overlay-class="motionClass" :show="Boolean(draft)" :title="t(draft?.id ? 'channelMonitorV2.unified.probes.edit' : 'channelMonitorV2.unified.probes.create')" @close="closeEditor">
      <form v-if="draft" :id="formId" data-testid="target-form" class="space-y-4" @submit.prevent="save">
        <p v-if="validation" role="alert" class="text-sm text-red-600">{{ validation }}</p>
        <div v-if="conflict" role="alert" class="space-y-2 rounded-xl bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-950/30 dark:text-amber-200"><p>{{ text('目标已被其他操作修改。当前草稿保留；请载入最新目标后再合并。', 'The target changed on the server. Your draft is retained; reload the latest target and merge your edits.') }}</p><button type="button" class="btn btn-secondary btn-sm" :disabled="Boolean(busy)" @click="reloadTarget">{{ text('放弃草稿，载入最新目标', 'Discard draft and reload target') }}</button></div>
        <fieldset :disabled="Boolean(busy)" class="space-y-4">
        <div><label :for="formId + '-group'" class="input-label">{{ t('channelMonitorV2.unified.probes.group') }}</label><Select :dropdown-class="motionClass" :id="formId + '-group'" :aria-label="t('channelMonitorV2.unified.probes.group')" v-model="draft.group_id" data-testid="target-group" :options="groupOptions" :disabled="Boolean(busy)" /></div>
        <label class="block space-y-1 text-sm"><span>{{ t('channelMonitorV2.unified.probes.model') }}</span><input v-model.trim="draft.model" data-testid="target-model" class="input" maxlength="192" required /></label>
        <div><label :for="formId + '-protocol'" class="input-label">{{ t('channelMonitorV2.unified.probes.protocol') }}</label><Select :dropdown-class="motionClass" :id="formId + '-protocol'" :aria-label="t('channelMonitorV2.unified.probes.protocol')" v-model="draft.protocol" :options="protocols" :searchable="false" :disabled="Boolean(busy)" /></div>
        <label class="inline-flex items-center gap-2 text-sm"><input v-model="draft.enabled" type="checkbox" class="checkbox" />{{ t('channelMonitorV2.unified.probes.enabled') }}</label>
        </fieldset>
      </form>
      <template #footer><div class="flex justify-end gap-2"><button type="button" class="btn btn-secondary" :disabled="Boolean(busy)" @click="closeEditor">{{ t('common.cancel') }}</button><button type="submit" :form="formId" class="btn btn-primary" :disabled="Boolean(busy) || conflict">{{ t('common.save') }}</button></div></template>
    </BaseDialog>
    <ConfirmDialog :overlay-class="motionClass" :show="Boolean(confirmTarget)" :title="t('channelMonitorV2.unified.probes.confirm')" :message="t('channelMonitorV2.unified.probes.confirmCost')" @cancel="confirmTarget = null" @confirm="run" />
  </section>
</template>
<script setup lang="ts">
import { computed, getCurrentInstance, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import Select from '@/components/common/Select.vue'
import { useMonitorMotion } from './useMonitorPreferences'
import { cloneSettings, monitorSettingsPreview, previewGroups } from './monitorSettingsPreview'
import { integerBetween, validModel } from './monitorSettingsValidation'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import { adminAPI } from '@/api/admin'
import { createProbeTarget, getProbeBudget, getProbeTargets, runProbeTarget, updateProbeTarget, type MonitorProbeBudget, type MonitorProbeProtocol, type MonitorProbeRun, type MonitorProbeTarget } from '@/api/channelMonitorV2'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
const motionClass = useMonitorMotion()
const props = withDefaults(defineProps<{ preview?: boolean }>(), { preview: false })
const { t, locale } = useI18n()
const text = (zh: string, en: string) => (locale?.value || 'zh').startsWith('zh') ? zh : en
const formId = `probe-target-${getCurrentInstance()?.uid}`
const conflict = ref(false)
const validation = ref('')
const savedMessage = ref('')
const app = useAppStore()
const targets = ref<MonitorProbeTarget[]>([])
const budget = ref<MonitorProbeBudget | null>(null)
const groups = ref<Array<{ id: number; name: string }>>([])
const loading = ref(true)
const failed = ref(false)
const busy = ref<number | 'save' | null>(null)
const draft = ref<(Pick<MonitorProbeTarget, 'group_id' | 'model' | 'protocol' | 'enabled'> & Partial<MonitorProbeTarget>) | null>(null)
const confirmTarget = ref<MonitorProbeTarget | null>(null)
const runs = ref<Record<number, MonitorProbeRun>>({})
const idempotencyKeys = new Map<number, string>()
const protocols: Array<{ value: MonitorProbeProtocol; label: string }> = [{ value: 'openai_responses', label: 'OpenAI Responses' }, { value: 'openai_chat', label: 'OpenAI Chat' }, { value: 'anthropic', label: 'Anthropic Messages' }, { value: 'gemini', label: 'Gemini' }]
const groupOptions = computed(() => [{ value: 0, label: t('channelMonitorV2.unified.probes.group'), disabled: true }, ...groups.value.map(g => ({ value: g.id, label: g.name })), ...(draft.value?.group_id && !groups.value.some(g => g.id === draft.value?.group_id) ? [{ value: draft.value.group_id, label: `#${draft.value.group_id}` }] : [])])
const protocolName = (value: string) => protocols.find(item => item.value === value)?.label || value
const groupName = (id: number) => groups.value.find(group => group.id === id)?.name || `#${id}`
const globalExhausted = computed(() => Boolean(budget.value && budget.value.global_used >= budget.value.global_limit))
const exhausted = (id: number) => { const value = budget.value?.targets.find(target => target.target_id === id); return !budget.value || globalExhausted.value || Boolean(value && value.used >= value.limit) }
const targetBudget = (id: number) => { const value = budget.value?.targets.find(target => target.target_id === id); return value ? `${value.used} / ${value.limit}` : '-' }
let epoch = 0
let alive = true
onBeforeUnmount(() => { epoch++; alive = false })
async function load() {
  const request = ++epoch
  loading.value = true
  failed.value = false
  try {
    const [result, limits, available] = props.preview
      ? [{ items: monitorSettingsPreview.getTargets() }, monitorSettingsPreview.getBudget(), previewGroups]
      : await Promise.all([getProbeTargets(), getProbeBudget(), adminAPI.groups.getAll()])
    if (request !== epoch) return
    targets.value = result.items
    budget.value = limits
    groups.value = available
  } catch (error) { if (request === epoch) { failed.value = true; app.showError(extractApiErrorMessage(error, t('channelMonitorV2.unified.loadFailed'))) } }
  finally { if (request === epoch) loading.value = false }
}
function edit(target?: MonitorProbeTarget) {
  if (busy.value) return
  conflict.value = false
  validation.value = ''
  draft.value = target ? cloneSettings(target) : { group_id: 0, model: '', protocol: 'openai_responses', enabled: false }
}
function closeEditor() { if (!busy.value) draft.value = null }
async function reloadTarget() {
  const id = draft.value?.id
  await load()
  if (failed.value) return
  const latest = targets.value.find(target => target.id === id)
  if (latest) edit(latest)
  else { conflict.value = false; draft.value = null }
}
async function save() {
  if (!draft.value || busy.value || conflict.value) return
  if (!integerBetween(draft.value.group_id, 1, Number.MAX_SAFE_INTEGER) || !validModel(draft.value.model.trim()) || !protocols.some(p => p.value === draft.value?.protocol)) { validation.value = text('请选择有效分组、协议和模型名；模型最多192字符，不接受网址或空格。', 'Select a valid group, protocol and model; maximum 192 characters, without URLs or whitespace.'); return }
  validation.value = ''
  savedMessage.value = ''
  busy.value = 'save'
  try {
    const payload = cloneSettings({ ...draft.value, model: draft.value.model.trim() })
    if (props.preview) monitorSettingsPreview.saveTarget(payload)
    else if (payload.id) await updateProbeTarget(payload as MonitorProbeTarget)
    else await createProbeTarget({ group_id: payload.group_id, model: payload.model, protocol: payload.protocol, enabled: payload.enabled })
    if (!alive) return
    draft.value = null
    savedMessage.value = props.preview ? text('上次目标保存成功（本地预览，不执行探测）。', 'Last target save succeeded locally; no probe was executed.') : t('channelMonitorV2.unified.probes.saved')
    app.showSuccess(props.preview ? text('已保存本地预览目标', 'Local preview target saved') : t('channelMonitorV2.unified.probes.saved'))
    await load()
  } catch (error) {
    if (!alive) return
    app.showError(extractApiErrorMessage(error, t('channelMonitorV2.settings.saveFailed')))
    conflict.value = (error as { response?: { status?: number } }).response?.status === 409
  } finally { busy.value = null }
}
async function toggle(target: MonitorProbeTarget) {
  if (busy.value) return
  busy.value = target.id
  try {
    if (props.preview) monitorSettingsPreview.saveTarget({ ...target, enabled: !target.enabled })
    else await updateProbeTarget({ ...target, enabled: !target.enabled })
    if (alive) { savedMessage.value = props.preview ? text('目标状态已在本地预览更新。', 'Target state updated in the local preview.') : t('channelMonitorV2.unified.probes.saved'); await load() }
  }
  catch (error) { if (alive) { app.showError(extractApiErrorMessage(error, t('channelMonitorV2.settings.saveFailed'))); await load() } }
  finally { busy.value = null }
}
async function run() {
  const target = confirmTarget.value
  if (props.preview || !target || busy.value || exhausted(target.id)) return
  confirmTarget.value = null
  busy.value = target.id
  const key = idempotencyKeys.get(target.id) || crypto.randomUUID()
  idempotencyKeys.set(target.id, key)
  try {
    const result = await runProbeTarget(target.id, key)
    if (!alive) return
    runs.value[target.id] = result
    idempotencyKeys.delete(target.id)
    app.showSuccess(t('channelMonitorV2.unified.probes.completed'))
    try { budget.value = await getProbeBudget() } catch { budget.value = null }
  } catch (error) { if (alive) app.showError(extractApiErrorMessage(error, t('channelMonitorV2.unified.probes.failed'))) }
  finally { busy.value = null }
}
onMounted(load)
</script>
