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
          <ObservationCurrentStatus :status="selectedModel ? selectedModel.current_status : item.current_status" :stale="stale" />
          <section>
            <h3 class="mb-3 text-sm font-semibold">{{ t('channelMonitorV2.unified.traffic') }}</h3>
            <ObservationMetrics v-if="selectedMetrics" :metrics="selectedMetrics" :health="selectedHealth" />
            <p v-else class="text-sm text-gray-500">{{ t('channelMonitorV2.observation.states.no_samples') }}</p>
          </section>
          <section v-if="!selection && !model && item.models.length" class="border-t border-gray-200 pt-4 dark:border-dark-700">
            <h3 class="mb-3 text-sm font-semibold">{{ t('channelMonitorV2.observation.modelDetails') }}</h3>
            <div v-for="model in item.models" :key="model.model" class="space-y-2 border-b border-gray-100 py-3 last:border-0 dark:border-dark-700">
              <p class="break-all text-sm font-medium">{{ model.model }}</p>
              <ObservationCurrentStatus :status="model.current_status" :stale="stale" />
              <ObservationMetrics :metrics="model.traffic || model.metrics" :health="model.health" />
            </div>
          </section>
          <section v-if="admin" class="border-t border-gray-200 pt-4 dark:border-dark-700">
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
              <ol class="divide-y divide-gray-100 text-xs dark:divide-dark-700"><li v-for="(sample, index) in accounts.samples" :key="index" class="space-y-1 py-3"><p class="flex flex-wrap justify-between gap-2"><span class="break-all font-medium">{{ sample.model }}</span><time class="text-gray-500">{{ formatTime(sample.completed_at) }}</time></p><p class="text-gray-500">{{ sample.outcome }} · {{ sample.http_status }}<span v-if="sample.error_category"> · {{ sample.error_category }}</span><span v-if="sample.account_id"> · #{{ sample.account_id }}</span></p></li></ol>
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
const props = withDefaults(defineProps<{ show: boolean; item: ObservationChannel | null; model?: string; selection: { start: string; bucket: ObservationBucket | null } | null; coverage?: ObservationOverview['coverage']; admin?: boolean; stale?: boolean; filter: MonitorFilter; identity: string }>(), { admin: false, stale: false })
const emit = defineEmits<{ close: [] }>()
const { t, locale } = useI18n()
const panel = ref<HTMLElement | null>(null)
const titleId = `monitor-detail-${getCurrentInstance()?.uid}`
const accounts = shallowRef<MonitorAccountDetail | null>(null)
const accountLoading = ref(false)
const accountError = ref(false)
const selectedModel = computed(() => props.model ? props.item?.models.find(model => model.model === props.model) : null)
const selectedMetrics = computed(() => props.selection ? props.selection.bucket?.metrics : selectedModel.value?.traffic || selectedModel.value?.metrics || props.item?.traffic || props.item?.metrics)
const selectedHealth = computed(() => (props.selection ? props.selection.bucket?.health : selectedModel.value?.health || props.item?.health) || { reliability: 'unknown' as const, latency: 'unknown' as const })
const formatTime = (value: string) => Date.parse(value) > 0 ? new Date(value).toLocaleString(locale.value) : '-'
let controller: AbortController | null = null
let previousFocus: HTMLElement | null = null
async function loadAccounts() {
  controller?.abort()
  accounts.value = null
  accountError.value = false
  if (!props.show || !props.admin || !props.item) { accountLoading.value = false; return }
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
watch(() => [props.show, props.admin, props.identity, props.item?.group_id, props.model, JSON.stringify(props.filter)], () => { void loadAccounts() }, { immediate: true, flush: 'sync' })
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
