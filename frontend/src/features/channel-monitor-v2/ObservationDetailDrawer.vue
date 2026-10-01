<template>
  <Teleport to="body">
    <div v-if="show && item" class="fixed inset-0 z-50 bg-black/30" @click.self="emit('close')">
      <aside ref="panel" role="dialog" aria-modal="true" :aria-labelledby="titleId" tabindex="-1" class="absolute inset-y-0 right-0 flex w-full max-w-xl flex-col bg-white shadow-xl outline-none dark:bg-dark-900">
        <header class="flex items-start justify-between gap-3 border-b border-gray-200 p-5 dark:border-dark-700">
          <div class="min-w-0">
            <h2 :id="titleId" class="break-words text-base font-semibold text-gray-900 dark:text-gray-100">{{ item.group_name }}</h2>
            <p v-if="model" class="mt-1 break-all text-sm font-medium text-primary-600 dark:text-primary-400">{{ model }}</p>
            <p class="mt-1 text-xs text-gray-500">{{ selection ? formatTime(selection.start) : t(`channelMonitorV2.ranges.${filter.range}`) }}</p>
          </div>
          <button type="button" class="btn btn-secondary btn-icon h-8 w-8 shrink-0" :aria-label="t('common.close')" @click="emit('close')"><Icon name="x" size="sm" /></button>
        </header>
        <div class="min-h-0 flex-1 space-y-6 overflow-y-auto p-5">
          <nav v-if="admin" class="flex flex-wrap gap-1 rounded-xl bg-gray-100 p-1 dark:bg-dark-800" :aria-label="t('channelMonitorV2.detail.sections')">
            <button v-for="tab in adminTabs" :key="tab.value" :data-testid="`detail-tab-${tab.value}`" :aria-pressed="detailTab === tab.value" type="button" class="rounded-lg px-3 py-1.5 text-xs font-medium" :class="detailTab === tab.value ? 'bg-white text-gray-900 shadow-sm dark:bg-dark-700 dark:text-white' : 'text-gray-500 dark:text-gray-400'" @click="detailTab = tab.value">{{ tab.label }}</button>
          </nav>
          <template v-if="!admin || detailTab === 'overview'">
            <ObservationCurrentStatus :status="selectedModel ? selectedModel.current_status : item.current_status" :stale="stale" />
            <section>
              <h3 class="mb-3 text-sm font-semibold">{{ t('channelMonitorV2.unified.traffic') }}</h3>
              <ObservationMetrics v-if="selectedMetrics" :metrics="selectedMetrics" :health="selectedHealth" />
              <p v-else class="text-sm text-gray-500">{{ t('channelMonitorV2.observation.states.no_samples') }}</p>
            </section>
          </template>
          <section v-if="(!admin || detailTab === 'overview') && !selection && !model && item.models.length" class="border-t border-gray-200 pt-4 dark:border-dark-700">
            <h3 class="mb-3 text-sm font-semibold">{{ t('channelMonitorV2.observation.modelDetails') }}</h3>
            <div v-for="model in item.models" :key="model.model" class="space-y-2 border-b border-gray-100 py-3 last:border-0 dark:border-dark-700">
              <p class="break-all text-sm font-medium">{{ model.model }}</p>
              <ObservationCurrentStatus :status="model.current_status" :stale="stale" />
              <ObservationMetrics :metrics="model.traffic || model.metrics" :health="model.health" />
            </div>
          </section>
          <section v-if="admin && detailTab === 'models'" class="border-t border-gray-200 pt-4 dark:border-dark-700">
            <h3 class="mb-3 text-sm font-semibold">{{ t('channelMonitorV2.detail.models') }}</h3>
            <div v-if="!item.models.length" class="text-sm text-gray-500">{{ t('channelMonitorV2.detail.noModels') }}</div>
            <div v-for="modelItem in item.models" :key="modelItem.model" class="space-y-2 border-b border-gray-100 py-3 last:border-0 dark:border-dark-700">
              <div class="flex items-center justify-between gap-3"><p class="break-all text-sm font-medium">{{ modelItem.model }}</p><span class="badge badge-gray">{{ t(`channelMonitorV2.observation.states.${modelItem.metrics.sample_state}`) }}</span></div>
              <ObservationCurrentStatus :status="modelItem.current_status" :stale="stale" />
              <ObservationMetrics :metrics="modelItem.traffic || modelItem.metrics" :health="modelItem.health" />
            </div>
          </section>
          <section v-if="admin && detailTab === 'trend'" class="border-t border-gray-200 pt-4 dark:border-dark-700">
            <h3 class="mb-3 text-sm font-semibold">{{ t('channelMonitorV2.detail.trend') }}</h3>
            <MonitorTrendPanel :items="trendItems" :coverage="coverage || emptyCoverage" :source="source" />
          </section>
          <section v-if="admin && detailTab === 'errors'" class="border-t border-gray-200 pt-4 dark:border-dark-700">
            <h3 class="mb-3 text-sm font-semibold">{{ t('channelMonitorV2.detail.errors') }}</h3>
            <dl v-if="errorEntries.length" class="space-y-2 text-xs"><div v-for="entry in errorEntries" :key="entry[0]" class="flex items-center justify-between gap-3"><dt class="text-gray-500">{{ categoryLabel(entry[0]) }}</dt><dd class="font-medium tabular-nums">{{ entry[1] }}</dd></div></dl>
            <p v-else class="text-sm text-gray-500" data-testid="detail-error-empty">{{ errorEmptyLabel }}</p>
          </section>
          <section v-if="admin && detailTab === 'probe'" class="border-t border-gray-200 pt-4 dark:border-dark-700">
            <h3 class="mb-3 text-sm font-semibold">{{ t('channelMonitorV2.detail.probeEvidence') }}</h3>
            <ObservationCurrentStatus :status="item.current_status?.source === 'probe' ? item.current_status : undefined" :stale="stale" />
            <p class="mt-2 text-xs text-gray-500">{{ item.probe ? `${statusLabel(item.probe.status)} · ${t('channelMonitorV2.detail.consecutiveFailures', { count: item.probe.consecutive_failures })}` : t('channelMonitorV2.detail.noProbe') }}</p>
          </section>
          <section v-if="admin && detailTab === 'quota'" class="border-t border-gray-200 pt-4 dark:border-dark-700">
            <h3 class="mb-3 text-sm font-semibold">{{ t('channelMonitorV2.detail.quotaSync') }}</h3>
            <p class="text-sm" :class="item.quota?.status === 'error' ? 'text-red-600' : 'text-gray-700 dark:text-gray-200'">{{ item.quota ? statusLabel(item.quota.status) : t('channelMonitorV2.detail.noQuota') }}</p>
            <p v-if="item.quota?.updated_at" class="mt-1 text-xs text-gray-500">{{ t('channelMonitorV2.updatedTo', { time: formatTime(item.quota.updated_at) }) }}</p>
          </section>
          <section v-if="admin && detailTab === 'quality'" class="border-t border-gray-200 pt-4 dark:border-dark-700">
            <h3 class="mb-3 text-sm font-semibold">{{ t('channelMonitorV2.detail.quality') }}</h3>
            <dl class="grid grid-cols-2 gap-3 text-xs"><div><dt class="text-gray-500">{{ t('channelMonitorV2.evidence.source') }}</dt><dd class="mt-1 font-medium">{{ monitorSourceLabel(item.source || source, t, te) }}</dd></div><div><dt class="text-gray-500">{{ t('channelMonitorV2.evidence.coverage') }}</dt><dd class="mt-1 font-medium">{{ coverage ? t(`channelMonitorV2.observation.coverage.${coverage.state}`) : '—' }}</dd></div><div><dt class="text-gray-500">{{ t('channelMonitorV2.evidence.aggregationLag') }}</dt><dd class="mt-1 font-medium">{{ coverage?.aggregation_lag_seconds != null ? t('channelMonitorV2.evidence.seconds', { seconds: coverage.aggregation_lag_seconds }) : '—' }}</dd></div><div><dt class="text-gray-500">{{ t('channelMonitorV2.evidence.gapReasons') }}</dt><dd class="mt-1 font-medium">{{ coverage?.gap_reasons?.length ? coverage.gap_reasons.map(gapLabel).join(' · ') : t('channelMonitorV2.evidence.noGaps') }}</dd></div></dl>
          </section>
          <section v-if="admin && owner && detailTab === 'overview'" class="border-t border-gray-200 pt-4 dark:border-dark-700">
            <h3 class="mb-3 text-sm font-semibold">{{ t('channelMonitorV2.unified.accounts') }} <span class="font-normal text-gray-500">{{ t(`channelMonitorV2.ranges.${filter.range}`) }}</span></h3>
            <p v-if="accountLoading" class="text-sm text-gray-500">{{ t('common.loading') }}</p>
            <div v-else-if="accountError" role="alert" class="flex items-center justify-between gap-3 text-sm text-amber-700"><span>{{ t('channelMonitorV2.unified.loadFailed') }}</span><button class="btn btn-secondary btn-sm" type="button" @click="loadAccounts">{{ t('common.retry') }}</button></div>
            <p v-else-if="!accounts?.items.length" class="text-sm text-gray-500">{{ t('channelMonitorV2.observation.empty') }}</p>
            <div v-else class="overflow-x-auto">
              <table class="w-full text-left text-xs">
                <thead><tr class="border-b border-gray-200 text-gray-500 dark:border-dark-700"><th class="py-2">{{ t('channelMonitorV2.unified.account') }}</th><th class="p-2">{{ t('channelMonitorV2.observation.attempts') }}</th><th class="p-2">{{ t('channelMonitorV2.unified.successfulAttempts') }}</th><th class="py-2">{{ t('channelMonitorV2.unified.failedAttempts') }}</th></tr></thead>
                <tbody><tr v-for="account in accounts.items" :key="account.account_id" class="border-b border-gray-100 dark:border-dark-700"><td class="py-3 font-medium">#{{ account.account_id }}</td><td class="p-2 tabular-nums">{{ account.metrics.attempt_count ?? 0 }}</td><td class="p-2 tabular-nums">{{ account.metrics.success_requests ?? 0 }}</td><td class="py-2 tabular-nums">{{ account.metrics.channel_errors ?? 0 }}</td></tr></tbody>
              </table>
            </div>
            <template v-if="accounts?.samples.length">
              <h4 class="mb-2 mt-5 text-xs font-semibold">{{ t('channelMonitorV2.unified.recentEvents') }}</h4>
              <ol class="divide-y divide-gray-100 text-xs dark:divide-dark-700"><li v-for="(sample, index) in accounts.samples" :key="index" class="space-y-1 py-3"><p class="flex flex-wrap justify-between gap-2"><span class="break-all font-medium">{{ sample.model }}</span><time class="text-gray-500">{{ formatTime(sample.completed_at) }}</time></p><p class="text-gray-500">{{ monitorOutcomeLabel(sample.outcome, t, te) }} · {{ sample.http_status }}<span v-if="sample.error_category"> · {{ categoryLabel(sample.error_category) }}</span><span v-if="sample.account_id"> · #{{ sample.account_id }}</span></p></li></ol>
            </template>
          </section>
        </div>
      </aside>
    </div>
  </Teleport>
</template>
<script setup lang="ts">
import { computed, getCurrentInstance, nextTick, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { getMonitorAccountDetail, type MonitorAccountDetail, type MonitorFilter, type ObservationBucket, type ObservationChannel, type ObservationOverview } from '@/api/channelMonitorV2'
import ObservationCurrentStatus from './ObservationCurrentStatus.vue'
import ObservationMetrics from './ObservationMetrics.vue'
import MonitorTrendPanel from './MonitorTrendPanel.vue'
import { monitorCategoryLabel, monitorOutcomeLabel, monitorSourceLabel } from './monitorLabels'
const props = withDefaults(defineProps<{ show: boolean; item: ObservationChannel | null; model?: string; selection: { start: string; bucket: ObservationBucket | null } | null; coverage?: ObservationOverview['coverage']; source?: ObservationOverview['source']; admin?: boolean; owner?: boolean; preview?: boolean; stale?: boolean; filter: MonitorFilter; identity: string }>(), { admin: false, owner: false, preview: false, stale: false })
const emit = defineEmits<{ close: [] }>()
const { t, te, locale } = useI18n()
const text = (zh: string, en: string) => locale.value.startsWith('zh') ? zh : en
const panel = ref<HTMLElement | null>(null)
const titleId = `monitor-detail-${getCurrentInstance()?.uid}`
const accounts = shallowRef<MonitorAccountDetail | null>(null)
const accountLoading = ref(false)
const accountError = ref(false)
const detailTab = ref<'overview' | 'models' | 'trend' | 'errors' | 'probe' | 'quota' | 'quality'>('overview')
const adminTabs = computed(() => (['overview', 'models', 'trend', 'errors', 'probe', 'quota', 'quality'] as const).map(value => ({ value, label: t(`channelMonitorV2.detail.${value}`) })))
const errorEntries = computed(() => Object.entries(selectedMetrics.value?.error_categories || {}).sort((a, b) => b[1] - a[1]))
const emptyCoverage = computed(() => props.coverage || ({ requested_start: new Date(0).toISOString(), requested_end: new Date(0).toISOString(), coverage_start: new Date(0).toISOString(), data_through: new Date(0).toISOString(), computed_at: new Date(0).toISOString(), aggregation_lag_seconds: 0, coverage_complete: false, bucket_seconds: 3600, state: 'unavailable', detail_retention_hours: 0, unsupported_protocols: [] } as ObservationOverview['coverage']))
const selectedModel = computed(() => props.model ? props.item?.models.find(model => model.model === props.model) : null)
const selectedMetrics = computed(() => props.selection ? props.selection.bucket?.metrics : selectedModel.value?.traffic || selectedModel.value?.metrics || props.item?.traffic || props.item?.metrics)
// A selected model owns a different history from its parent channel.
const trendItems = computed<ObservationChannel[]>(() => {
  if (!props.item) return []
  const selected = selectedModel.value
  return selected ? [{ ...props.item, group_name: `${props.item.group_name} · ${selected.model}`, metrics: selected.metrics, traffic: selected.traffic, health: selected.health, buckets: selected.buckets, models: [selected] }] : [props.item]
})
const errorEmptyLabel = computed(() => {
  if (props.selection && !props.selection.bucket) return t('channelMonitorV2.observation.states.missing')
  if (props.coverage?.state === 'unavailable') return t('channelMonitorV2.observation.states.unavailable')
  if (!selectedMetrics.value || selectedMetrics.value.sample_state === 'no_samples') return t('channelMonitorV2.observation.states.no_samples')
  return selectedMetrics.value.channel_errors === 0
    ? text('已观测真实流量中没有渠道错误。', 'No channel errors in observed traffic.')
    : text('当前范围没有已分类错误，不能据此推断错误率为零。', 'No classified errors in this range; this does not imply a zero error rate.')
})
const gapLabel = (reason: string) => {
  const key = `channelMonitorV2.unified.gaps.${reason}`
  return te(key) ? t(key) : reason
}
const categoryLabel = (category: string) => monitorCategoryLabel(category, t, te)
const statusLabel = (status: string) => {
  const key = `channelMonitorV2.unified.states.${status}`
  return te(key) ? t(key) : status
}

const selectedHealth = computed(() => (props.selection ? props.selection.bucket?.health : selectedModel.value?.health || props.item?.health) || { reliability: 'unknown' as const, latency: 'unknown' as const })
const formatTime = (value: string) => Date.parse(value) > 0 ? new Date(value).toLocaleString(locale.value) : '-'
let controller: AbortController | null = null
let previousFocus: HTMLElement | null = null
async function loadAccounts() {
  controller?.abort()
  accounts.value = null
  accountError.value = false
  if (!props.show || !props.admin || !props.owner || props.preview || !props.item) { accountLoading.value = false; return }
  const current = new AbortController()
  controller = current
  accountLoading.value = true
  try {
    const result = await getMonitorAccountDetail(props.item.group_id, { ...props.filter, models: props.model ? [props.model] : [...props.filter.models] }, current.signal)
    if (!current.signal.aborted) accounts.value = result
  } catch {
    if (!current.signal.aborted) accountError.value = true
  } finally {
    if (controller === current) accountLoading.value = false
  }
}
watch(() => [props.show, props.admin, props.owner, props.preview, props.identity, props.item?.group_id, props.item?.platform, props.model, JSON.stringify(props.filter)], () => { void loadAccounts() }, { immediate: true, flush: 'sync' })
watch(() => props.show, show => { if (show) detailTab.value = 'overview' })
watch(() => props.show, async (show) => {
  if (show) {
    previousFocus = document.activeElement as HTMLElement | null
    await nextTick()
    panel.value?.focus()
  } else { previousFocus?.focus(); previousFocus = null }
}, { immediate: true })
function keydown(event: KeyboardEvent) {
  if (!props.show) return
  if (event.key === 'Escape') { event.preventDefault(); emit('close'); return }
  if (event.key !== 'Tab' || !panel.value) return
  const controls = [...panel.value.querySelectorAll<HTMLElement>('button:not(:disabled),a[href],input,select,textarea,[tabindex="0"]')]
  const first = controls[0]
  const last = controls.at(-1)
  if (event.shiftKey && (document.activeElement === first || document.activeElement === panel.value)) { event.preventDefault(); (last || panel.value).focus() }
  else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); (first || panel.value).focus() }
}
onMounted(() => document.addEventListener('keydown', keydown))
onBeforeUnmount(() => { controller?.abort(); document.removeEventListener('keydown', keydown); previousFocus?.focus() })
</script>
