<template>
  <article class="observation-group-card flex h-full min-w-0 flex-col" :class="[motionClass, { 'card-comfortable': preferences.density === 'comfortable' }]" :data-group-id="item.group_id">
    <header class="flex items-start gap-3">
      <span class="inline-flex h-10 w-10 shrink-0 items-center justify-center rounded-xl" :class="platformBadgeClass(item.platform)"><PlatformIcon :platform="platform" size="md" /></span>
      <div class="min-w-0 flex-1">
        <h3><button type="button" class="card-channel-name break-words text-left text-base font-semibold leading-6 tracking-tight text-slate-800 hover:text-primary-600 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-primary-500 dark:text-gray-100" @click="$emit('detail', item)">{{ item.group_name }}</button></h3>
        <div class="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-slate-500 dark:text-gray-400">
          <span class="rounded-md px-1.5 py-0.5 font-medium" :class="platformBadgeClass(item.platform)">{{ platformLabel }}</span>
          <span v-if="item.rate_multiplier != null" class="font-medium tabular-nums text-primary-600 dark:text-primary-400">{{ t('channelMonitorV2.observation.multiplier', { value: item.rate_multiplier }) }}</span>
          <span v-else>{{ t('channelMonitorV2.observation.priceUnavailable') }}</span>
        </div>
      </div>
      <ObservationCurrentStatus class="max-w-[100px] shrink-0" compact :status="item.current_status" :stale="stale" />
    </header>

    <div class="mb-2.5 mt-4 flex flex-wrap items-center justify-between gap-2 text-[11px] text-slate-500 dark:text-gray-400">
      <span class="inline-flex items-center gap-1.5"><span class="h-1.5 w-1.5 rounded-full bg-slate-300 dark:bg-dark-500" />{{ t('channelMonitorV2.unified.traffic') }}</span>
      <ObservationStatus :metrics="metrics" :health="item.health" :coverage="coverage" />
    </div>
    <ObservationMetrics variant="card" :metrics="metrics" :health="item.health" :unavailable="coverage.state === 'unavailable'" />

    <section class="mt-4">
      <div class="mb-2 flex flex-wrap items-center justify-between gap-2 text-[11px] text-slate-500 dark:text-gray-400">
        <span data-testid="card-history-interval">{{ t('channelMonitorV2.observation.history') }} · {{ t('channelMonitorV2.unified.historyInterval', { minutes: Math.round(coverage.bucket_seconds / 60) }) }} · {{ historyBarCount }} {{ text('条', 'bars') }}</span>
        <span v-if="admin && (item.source || source)" class="text-[10px]">{{ monitorSourceLabel(item.source || source, t, te) }}</span>
      </div>
      <ObservationTimeline variant="card" :buckets="item.buckets" :coverage="coverage" :admin="admin" @select="$emit('bucket', item, $event)" />
    </section>

    <dl class="mt-4 grid grid-cols-2 gap-4 border-t border-slate-100 pt-3 text-xs dark:border-dark-700">
      <div class="min-w-0"><dt class="text-slate-500 dark:text-gray-400">{{ t('channelMonitorV2.unified.probe') }}</dt><dd class="mt-1.5 flex items-center gap-1.5 font-medium text-slate-700 dark:text-gray-200"><span aria-hidden="true" class="h-1.5 w-1.5 rounded-full" :class="statusDot(item.probe?.status)" />{{ statusLabel(item.probe?.status) }}</dd></div>
      <div class="min-w-0"><dt class="text-slate-500 dark:text-gray-400">{{ t('channelMonitorV2.unified.quota') }}</dt><dd class="mt-1.5 flex items-center gap-1.5 font-medium text-slate-700 dark:text-gray-200"><span aria-hidden="true" class="h-1.5 w-1.5 rounded-full" :class="statusDot(item.quota?.status)" />{{ statusLabel(item.quota?.status) }}</dd></div>
    </dl>

    <dl v-if="admin" data-testid="card-traffic-counts" class="mt-4 grid grid-cols-2 gap-x-3 gap-y-2 border-t border-slate-100 pt-3 text-xs sm:grid-cols-3 dark:border-dark-700">
      <div><dt class="text-gray-400">{{ t('channelMonitorV2.observation.requests') }}</dt><dd class="mt-1 font-semibold tabular-nums text-gray-700 dark:text-gray-200">{{ trafficCount(metrics.request_count) }}</dd></div>
      <div><dt class="text-gray-400">{{ t('channelMonitorV2.observation.errors') }}</dt><dd class="mt-1 font-semibold tabular-nums text-red-600 dark:text-red-400">{{ trafficCount(metrics.channel_errors) }}</dd></div>
      <div><dt class="text-gray-400">{{ t('channelMonitorV2.observation.attempts') }}</dt><dd class="mt-1 font-semibold tabular-nums text-gray-700 dark:text-gray-200">{{ trafficCount(metrics.attempt_count) }}</dd></div>
    </dl>
    <section v-if="admin" class="mt-4 space-y-2 text-xs" data-testid="card-data-quality">
      <p class="text-gray-500 dark:text-gray-400">{{ text('聚合延迟', 'Aggregation lag') }} · <span class="tabular-nums">{{ aggregationLag }}</span></p>
      <p v-if="coverage.gap_reasons?.length" class="break-words text-amber-700 dark:text-amber-300">{{ coverage.gap_reasons.map(gapLabel).join(' · ') }}</p>
      <div class="border-t border-gray-100 pt-2 dark:border-dark-700"><p class="text-gray-400">{{ text('最近异常分类', 'Recent error categories') }}</p><p class="mt-1 break-words text-gray-600 dark:text-gray-300">{{ recentErrorLabel }}</p></div>
    </section>

    <footer class="mt-auto pt-3">
      <button type="button" class="card-model-toggle flex w-full items-center justify-between gap-2 rounded-lg py-1 text-xs font-medium text-slate-500 hover:text-primary-600 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-primary-500 dark:text-gray-400 dark:hover:text-primary-400" :aria-expanded="expanded" :aria-controls="modelsId" @click="toggleExpanded">
        <span>{{ t('channelMonitorV2.observation.models', { count: item.models.length }) }}</span><Icon name="chevronRight" size="xs" class="card-model-chevron" :class="expanded ? 'rotate-90' : ''" />
      </button>
      <Transition name="card-models">
      <div v-if="expanded" :id="modelsId" class="card-models-panel">
      <div class="min-h-0 overflow-hidden">
      <div class="mt-3 border-t border-slate-100 pt-3 dark:border-dark-700">
        <p class="mb-2 text-xs font-semibold text-gray-400">{{ t('channelMonitorV2.observation.modelDetails') }}</p>
        <div v-if="item.models.length" class="space-y-2">
          <div v-for="model in item.models" :key="model.model" class="flex items-center justify-between gap-3 rounded-xl bg-slate-50 px-3 py-2.5 text-xs dark:bg-dark-900/40">
            <span class="min-w-0 truncate font-medium text-gray-700 dark:text-gray-200">{{ model.model }}</span>
            <span class="shrink-0 tabular-nums text-gray-500 dark:text-gray-400">{{ percent(model.metrics.reliability_rate) }} · {{ formatMonitorMs(model.metrics.ttft.p50_ms) }}</span>
          </div>
        </div>
        <p v-else class="text-xs text-gray-400">{{ t('channelMonitorV2.observation.noModels') }}</p>
      </div>
      </div>
      </div>
      </Transition>
    </footer>
  </article>
</template>
<script setup lang="ts">
import { computed, getCurrentInstance, ref, watch } from 'vue'
import { useMonitorMotion, useMonitorPreferences } from './useMonitorPreferences'
import { monitorCategoryLabel, monitorSourceLabel } from './monitorLabels'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import { platformBadgeClass } from '@/utils/platformColors'
import { GROUP_PLATFORM_OPTIONS } from '@/constants/platforms'
import { formatMonitorMs, formatMonitorPercent } from './monitorFormat'
import { observationTimeline } from './observationViewModel'
import type { ObservationBucket, ObservationChannel, ObservationOverview } from '@/api/channelMonitorV2'
import ObservationMetrics from './ObservationMetrics.vue'
import ObservationTimeline from './ObservationTimeline.vue'
import ObservationStatus from './ObservationStatus.vue'
import ObservationCurrentStatus from './ObservationCurrentStatus.vue'
const props = withDefaults(defineProps<{ item: ObservationChannel; coverage: ObservationOverview['coverage']; source?: ObservationOverview['source']; admin?: boolean; stale?: boolean }>(), { admin: false, stale: false })
defineEmits<{ detail: [item: ObservationChannel]; bucket: [item: ObservationChannel, slot: { start: string; bucket: ObservationBucket | null }] }>()
const { t, te, locale } = useI18n()
const text = (zh: string, en: string) => locale.value.startsWith('zh') ? zh : en
const platform = computed(() => GROUP_PLATFORM_OPTIONS.find(p => p.value === props.item.platform)?.value)
const platformLabel = computed(() => { const key = `channelMonitorV2.platforms.${props.item.platform}`; return te(key) ? t(key) : GROUP_PLATFORM_OPTIONS.find(p => p.value === props.item.platform)?.label || props.item.platform })
const metrics = computed(() => props.item.traffic || props.item.metrics)
const historyBarCount = computed(() => observationTimeline([], props.coverage).length)
const hasTraffic = computed(() => metrics.value.sample_state !== 'no_samples' && props.coverage.state !== 'unavailable')
const trafficCount = (value: number | undefined) => hasTraffic.value && value != null ? String(value) : '—'
const aggregationLag = computed(() => {
  if (!Number.isFinite(props.coverage.aggregation_lag_seconds)) return '—'
  return locale.value.startsWith('zh') ? t('channelMonitorV2.evidence.seconds', { seconds: props.coverage.aggregation_lag_seconds }) : `${props.coverage.aggregation_lag_seconds}s`
})
const gapLabel = (reason: string) => {
  const key = `channelMonitorV2.unified.gaps.${reason}`
  return te(key) ? t(key) : reason
}
const recentErrorLabel = computed(() => {
  if (!hasTraffic.value) return t(`channelMonitorV2.observation.states.${props.coverage.state === 'unavailable' ? 'unavailable' : 'no_samples'}`)
  const entries = Object.entries(metrics.value.error_categories || {}).filter(([, count]) => count > 0).sort((a, b) => b[1] - a[1]).slice(0, 2)
  if (entries.length) return entries.map(([category, count]) => {
    return `${monitorCategoryLabel(category, t, te)} · ${count}`
  }).join(' / ')
  return metrics.value.channel_errors === 0 ? text('已观测流量无渠道错误', 'No observed channel errors') : text('暂无分类，不能推断为零错误', 'No classification; zero errors cannot be inferred')
})
const preferences = useMonitorPreferences()
const motionClass = useMonitorMotion()
const expanded = ref(preferences.value.expandModels)
watch(() => preferences.value.expandModels, value => { expanded.value = value })
const modelsId = `card-models-${getCurrentInstance()?.uid}`
const percent = (value: number | null | undefined) => value == null ? '-' : formatMonitorPercent(value)
const statusLabel = (value?: string) => {
  const key = `channelMonitorV2.unified.states.${value || 'unknown'}`
  return te(key) ? t(key) : value || t('channelMonitorV2.unified.states.unknown')
}
const statusDot = (value?: string) => ['healthy', 'available', 'ok'].includes(value || '') ? 'bg-emerald-500' : ['critical', 'error', 'unavailable', 'exhausted'].includes(value || '') ? 'bg-red-500' : ['warning', 'stale', 'degraded'].includes(value || '') ? 'bg-amber-500' : 'bg-slate-300 dark:bg-dark-500'
function toggleExpanded() {
  expanded.value = !expanded.value
}
</script>

<style scoped>
.observation-group-card {
  padding: 20px;
  border: 1px solid rgba(226, 232, 240, .65);
  border-radius: 24px;
  background: rgba(255, 255, 255, .94);
  box-shadow: 0 10px 32px -20px rgba(33, 77, 76, .22);
  transition: border-color 180ms ease, box-shadow 180ms ease, transform 180ms cubic-bezier(.2, .8, .2, 1);
}
.observation-group-card.card-comfortable { padding: 28px; border-radius: 28px; }
@media (hover: hover) and (pointer: fine) {
  .observation-group-card:hover {
    transform: translateY(-2px);
    border-color: rgba(148, 163, 184, .5);
    box-shadow: 0 12px 36px -20px rgba(33, 77, 76, .32);
  }
}
.card-channel-name, .card-model-toggle { transition: color 160ms ease, background-color 160ms ease, transform 160ms ease; }
.card-channel-name:active, .card-model-toggle:active { transform: scale(.98); }
.card-model-toggle:hover { background-color: rgb(148 163 184 / .07); }
.card-model-chevron { transition: transform 200ms cubic-bezier(.2, .8, .2, 1); }
.card-models-panel { display: grid; grid-template-rows: 1fr; }
.card-models-enter-active, .card-models-leave-active { transition: grid-template-rows 200ms ease, opacity 160ms ease; }
.card-models-enter-from, .card-models-leave-to { grid-template-rows: 0fr; opacity: 0; }
:global(.dark .observation-group-card) {
  background: #18222f;
  border-color: #2b394a;
  box-shadow: none;
}
@media (max-width: 480px) {
  .observation-group-card { padding: 18px; border-radius: 22px; }
}
@media (prefers-reduced-motion: reduce) {
  .observation-group-card, .card-channel-name, .card-model-toggle, .card-model-chevron, .card-models-panel { transition: none; }
  .observation-group-card:hover, .card-channel-name:active, .card-model-toggle:active { transform: none; }
}
</style>
