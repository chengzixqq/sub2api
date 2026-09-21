<template>
  <section class="space-y-4 border-b border-gray-200 pb-6 dark:border-dark-700">
    <header class="flex flex-wrap items-center justify-between gap-3">
      <div><h2 class="text-base font-semibold">{{ t('channelMonitorV2.unified.probes.title') }}</h2><p v-if="budget" class="mt-1 text-xs text-gray-500">{{ t('channelMonitorV2.unified.probes.budget') }} · {{ budget.utc_day }} · <span class="tabular-nums" :class="globalExhausted ? 'text-red-600' : ''">{{ t('channelMonitorV2.unified.probes.globalUsed', { used: budget.global_used, limit: budget.global_limit }) }}</span></p></div>
      <div class="flex items-center gap-2"><button type="button" class="btn btn-secondary btn-icon h-9 w-9" :disabled="loading" :aria-label="t('common.refresh')" :title="t('common.refresh')" @click="load"><Icon name="refresh" size="sm" /></button><button data-testid="add-target" type="button" class="btn btn-primary" :disabled="loading" @click="edit()"><Icon name="plus" size="sm" />{{ t('channelMonitorV2.unified.probes.add') }}</button></div>
    </header>
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
          <td class="py-3"><div class="flex justify-end gap-2"><button type="button" class="btn btn-secondary btn-icon h-8 w-8" :disabled="Boolean(busy)" :aria-label="t('channelMonitorV2.unified.probes.edit')" :title="t('channelMonitorV2.unified.probes.edit')" @click="edit(target)"><Icon name="edit" size="sm" /></button><button :data-testid="`probe-${target.id}`" type="button" class="btn btn-secondary btn-icon h-8 w-8" :disabled="Boolean(busy) || exhausted(target.id)" :aria-label="t('channelMonitorV2.unified.probes.run')" :title="t(exhausted(target.id) ? 'channelMonitorV2.unified.probes.budgetExhausted' : 'channelMonitorV2.unified.probes.run')" @click="confirmTarget = target"><Icon name="play" size="sm" /></button></div></td>
        </tr></tbody>
      </table>
    </div>
    <BaseDialog :show="Boolean(draft)" :title="t(draft?.id ? 'channelMonitorV2.unified.probes.edit' : 'channelMonitorV2.unified.probes.create')" @close="draft = null">
      <form v-if="draft" id="probe-target" data-testid="target-form" class="space-y-4" @submit.prevent="save">
        <label class="block space-y-1 text-sm"><span>{{ t('channelMonitorV2.unified.probes.group') }}</span><select v-model.number="draft.group_id" data-testid="target-group" class="input" required><option :value="0" disabled>{{ t('channelMonitorV2.unified.probes.group') }}</option><option v-for="group in groups" :key="group.id" :value="group.id">{{ group.name }}</option></select></label>
        <label class="block space-y-1 text-sm"><span>{{ t('channelMonitorV2.unified.probes.model') }}</span><input v-model.trim="draft.model" data-testid="target-model" class="input" maxlength="192" required /></label>
        <label class="block space-y-1 text-sm"><span>{{ t('channelMonitorV2.unified.probes.protocol') }}</span><select v-model="draft.protocol" class="input"><option v-for="protocol in protocols" :key="protocol.value" :value="protocol.value">{{ protocol.label }}</option></select></label>
        <label class="inline-flex items-center gap-2 text-sm"><input v-model="draft.enabled" type="checkbox" class="checkbox" />{{ t('channelMonitorV2.unified.probes.enabled') }}</label>
      </form>
      <template #footer><div class="flex justify-end gap-2"><button type="button" class="btn btn-secondary" :disabled="Boolean(busy)" @click="draft = null">{{ t('common.cancel') }}</button><button type="submit" form="probe-target" class="btn btn-primary" :disabled="Boolean(busy)">{{ t('common.save') }}</button></div></template>
    </BaseDialog>
    <ConfirmDialog :show="Boolean(confirmTarget)" :title="t('channelMonitorV2.unified.probes.confirm')" :message="t('channelMonitorV2.unified.probes.confirmCost')" @cancel="confirmTarget = null" @confirm="run" />
  </section>
</template>
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import { adminAPI } from '@/api/admin'
import { createProbeTarget, getProbeBudget, getProbeTargets, runProbeTarget, updateProbeTarget, type MonitorProbeBudget, type MonitorProbeProtocol, type MonitorProbeRun, type MonitorProbeTarget } from '@/api/channelMonitorV2'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
const { t } = useI18n()
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
const protocolName = (value: string) => protocols.find(item => item.value === value)?.label || value
const groupName = (id: number) => groups.value.find(group => group.id === id)?.name || `#${id}`
const globalExhausted = computed(() => Boolean(budget.value && budget.value.global_used >= budget.value.global_limit))
const exhausted = (id: number) => { const value = budget.value?.targets.find(target => target.target_id === id); return !budget.value || globalExhausted.value || Boolean(value && value.used >= value.limit) }
const targetBudget = (id: number) => { const value = budget.value?.targets.find(target => target.target_id === id); return value ? `${value.used} / ${value.limit}` : '-' }
async function load() {
  loading.value = true
  failed.value = false
  try {
    const [result, limits, available] = await Promise.all([getProbeTargets(), getProbeBudget(), adminAPI.groups.getAll()])
    targets.value = result.items
    budget.value = limits
    groups.value = available
  } catch (error) { failed.value = true; app.showError(extractApiErrorMessage(error, t('channelMonitorV2.unified.loadFailed'))) }
  finally { loading.value = false }
}
function edit(target?: MonitorProbeTarget) { draft.value = target ? { ...target } : { group_id: 0, model: '', protocol: 'openai_responses', enabled: false } }
async function save() {
  if (!draft.value || busy.value) return
  if (!draft.value.group_id || !draft.value.model.trim()) { app.showError(t('channelMonitorV2.unified.probes.invalid')); return }
  busy.value = 'save'
  try {
    if (draft.value.id) await updateProbeTarget(draft.value as MonitorProbeTarget)
    else await createProbeTarget({ group_id: draft.value.group_id, model: draft.value.model.trim(), protocol: draft.value.protocol, enabled: draft.value.enabled })
    draft.value = null
    app.showSuccess(t('channelMonitorV2.unified.probes.saved'))
    await load()
  } catch (error) {
    app.showError(extractApiErrorMessage(error, t('channelMonitorV2.settings.saveFailed')))
    if ((error as { response?: { status?: number } }).response?.status === 409) { draft.value = null; await load() }
  }
  finally { busy.value = null }
}
async function toggle(target: MonitorProbeTarget) {
  if (busy.value) return
  busy.value = target.id
  try { await updateProbeTarget({ ...target, enabled: !target.enabled }); await load() }
  catch (error) { app.showError(extractApiErrorMessage(error, t('channelMonitorV2.settings.saveFailed'))); await load() }
  finally { busy.value = null }
}
async function run() {
  const target = confirmTarget.value
  if (!target || busy.value || exhausted(target.id)) return
  confirmTarget.value = null
  busy.value = target.id
  const key = idempotencyKeys.get(target.id) || crypto.randomUUID()
  idempotencyKeys.set(target.id, key)
  try {
    runs.value[target.id] = await runProbeTarget(target.id, key)
    idempotencyKeys.delete(target.id)
    app.showSuccess(t('channelMonitorV2.unified.probes.completed'))
    try { budget.value = await getProbeBudget() } catch { budget.value = null }
  } catch (error) { app.showError(extractApiErrorMessage(error, t('channelMonitorV2.unified.probes.failed'))) }
  finally { busy.value = null }
}
onMounted(load)
</script>
