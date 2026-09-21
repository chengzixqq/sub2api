<template>
  <section class="space-y-4 border-b border-gray-200 pb-6 dark:border-dark-700">
    <header class="flex flex-wrap items-center justify-between gap-3"><h2 class="text-base font-semibold">{{ t('channelMonitorV2.unified.settings.title') }}</h2><button type="submit" form="observation-policy" class="btn btn-primary" :disabled="saving || !draft"><Icon name="check" size="sm" />{{ t('common.save') }}</button></header>
    <p v-if="loading" class="py-6 text-sm text-gray-500">{{ t('common.loading') }}</p>
    <div v-else-if="!draft" role="alert" class="flex items-center gap-3 text-sm text-amber-700"><span>{{ t('channelMonitorV2.unified.loadFailed') }}</span><button type="button" class="btn btn-secondary" @click="load">{{ t('common.retry') }}</button></div>
    <form v-else id="observation-policy" class="space-y-5" @submit.prevent="save">
      <div class="flex flex-wrap items-center gap-x-8 gap-y-4">
        <label class="inline-flex items-center gap-2 text-sm"><input v-model="draft.enabled" data-testid="collection-enabled" type="checkbox" class="checkbox" />{{ t('channelMonitorV2.unified.settings.enabled') }}</label>
        <label class="inline-flex items-center gap-2 text-sm"><input v-model="draft.probe_enabled" data-testid="probe-enabled" type="checkbox" class="checkbox" />{{ t('channelMonitorV2.unified.settings.probeEnabled') }}</label>
        <label class="inline-flex items-center gap-2 text-sm"><input v-model="draft.quota_enabled" data-testid="quota-enabled" type="checkbox" class="checkbox" />{{ t('channelMonitorV2.unified.settings.quotaEnabled') }}</label>
        <a href="/admin/settings" class="text-sm text-primary-600 hover:underline dark:text-primary-400">{{ t(displayEnabled ? 'channelMonitorV2.unified.settings.displayOn' : 'channelMonitorV2.unified.settings.displayOff') }}</a>
      </div>
      <div class="flex flex-wrap items-center gap-x-8 gap-y-4">
        <label class="flex items-center gap-3 text-sm"><span>{{ t('channelMonitorV2.unified.settings.mode') }}</span><select v-model="draft.mode" data-testid="publication" class="input w-auto"><option value="shadow">{{ t('channelMonitorV2.unified.settings.shadow') }}</option><option value="live">{{ t('channelMonitorV2.unified.settings.live') }}</option></select></label>
        <dl class="flex gap-6 text-xs text-gray-500"><div><dt>{{ t('channelMonitorV2.unified.settings.refresh') }}</dt><dd class="mt-1 font-medium text-gray-800 dark:text-gray-200">{{ t('channelMonitorV2.unified.settings.refreshValue') }}</dd></div><div><dt>{{ t('channelMonitorV2.unified.settings.retention') }}</dt><dd class="mt-1 font-medium text-gray-800 dark:text-gray-200">{{ t('channelMonitorV2.unified.settings.retentionValue') }}</dd></div></dl>
      </div>
      <fieldset v-if="draft.mode === 'live'" class="space-y-2"><legend class="mb-2 text-sm font-medium">{{ t('channelMonitorV2.unified.settings.liveGroups') }} <span v-if="!draft.live_group_ids?.length" class="badge badge-warning">{{ t('channelMonitorV2.unified.settings.allGroups') }}</span></legend><div class="flex max-h-48 flex-wrap gap-x-5 gap-y-3 overflow-y-auto"><label v-for="group in groups" :key="group.id" class="inline-flex items-center gap-2 text-sm"><input :data-testid="`live-group-${group.id}`" type="checkbox" class="checkbox" :checked="draft.live_group_ids?.includes(group.id)" @change="toggleGroup(group.id)" /><span>{{ group.name }}</span></label></div></fieldset>
      <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-5">
        <label class="space-y-1 text-xs text-gray-600 dark:text-gray-300"><span>{{ t('channelMonitorV2.unified.settings.minimumSample') }}</span><input v-model.number="draft.minimum_sample" class="input" type="number" min="1" max="1000000" required /></label>
        <label class="space-y-1 text-xs text-gray-600 dark:text-gray-300"><span>{{ t('channelMonitorV2.unified.settings.healthyReliability') }}</span><input v-model.number="healthyPercent" class="input" type="number" min="0.01" max="100" step="0.01" required /></label>
        <label class="space-y-1 text-xs text-gray-600 dark:text-gray-300"><span>{{ t('channelMonitorV2.unified.settings.warningReliability') }}</span><input v-model.number="warningPercent" class="input" type="number" min="0.01" :max="healthyPercent - 0.01" step="0.01" required /></label>
        <label class="space-y-1 text-xs text-gray-600 dark:text-gray-300"><span>{{ t('channelMonitorV2.unified.settings.warningTtft') }}</span><input v-model.number="draft.warning_ttft_ms" class="input" type="number" min="1" :max="draft.critical_ttft_ms - 1" required /></label>
        <label class="space-y-1 text-xs text-gray-600 dark:text-gray-300"><span>{{ t('channelMonitorV2.unified.settings.criticalTtft') }}</span><input v-model.number="draft.critical_ttft_ms" class="input" type="number" :min="draft.warning_ttft_ms + 1" max="86400000" required /></label>
      </div>
    </form>
  </section>
</template>
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { adminAPI } from '@/api/admin'
import { getObservationConfig, updateObservationConfig, type ObservationConfig } from '@/api/channelMonitorV2'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import { isChannelMonitorRouteEnabled } from '@/utils/featureFlags'
const { t } = useI18n()
const app = useAppStore()
const loading = ref(true)
const saving = ref(false)
const draft = ref<ObservationConfig | null>(null)
const groups = ref<Array<{ id: number; name: string }>>([])
const displayEnabled = computed(isChannelMonitorRouteEnabled)
const healthyPercent = computed({ get: () => (draft.value?.healthy_reliability || .99) * 100, set: (value: number) => { if (draft.value) draft.value.healthy_reliability = value / 100 } })
const warningPercent = computed({ get: () => (draft.value?.warning_reliability || .95) * 100, set: (value: number) => { if (draft.value) draft.value.warning_reliability = value / 100 } })
async function load() {
  loading.value = true
  try {
    const [policy, available] = await Promise.all([getObservationConfig(), adminAPI.groups.getAll()])
    draft.value = { ...policy, probe_enabled: policy.probe_enabled ?? false, quota_enabled: policy.quota_enabled ?? true, live_group_ids: [...(policy.live_group_ids || [])], overrides: policy.overrides.map(value => ({ ...value })) }
    groups.value = available
  } catch (error) { app.showError(extractApiErrorMessage(error, t('channelMonitorV2.unified.loadFailed'))) }
  finally { loading.value = false }
}
function toggleGroup(id: number) {
  if (!draft.value) return
  const selected = new Set(draft.value.live_group_ids)
  if (selected.has(id)) selected.delete(id)
  else selected.add(id)
  draft.value.live_group_ids = [...selected].sort((a, b) => a - b)
}
async function save() {
  if (!draft.value || saving.value) return
  saving.value = true
  try {
    draft.value = await updateObservationConfig({ ...draft.value, detail_retention_hours: 24, refresh_interval_seconds: 60 })
    app.showSuccess(t('channelMonitorV2.unified.settings.saved'))
  } catch (error) {
    app.showError(extractApiErrorMessage(error, t('channelMonitorV2.settings.saveFailed')))
    if ((error as { response?: { status?: number } }).response?.status === 409) await load()
  } finally { saving.value = false }
}
onMounted(load)
</script>
