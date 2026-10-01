<template>
  <article class="card flex h-full min-w-0 flex-col !rounded-lg p-4" :data-group-id="item.group_id">
    <div class="flex items-start gap-3">
      <span class="inline-flex h-10 w-10 shrink-0 items-center justify-center rounded-lg" :class="platformBadgeClass(item.platform)"><PlatformIcon :platform="platform" size="md" /></span>
      <div class="min-w-0 flex-1">
        <button type="button" class="break-words text-left text-sm font-semibold leading-6 text-gray-900 hover:text-primary-600 focus-visible:outline-primary-500 dark:text-gray-100" @click="$emit('detail', item)">{{ item.group_name }}</button>
        <div class="mt-1 flex flex-wrap gap-x-2 gap-y-1 text-xs text-gray-500 dark:text-gray-400">
          <span>{{ platformLabel }}</span>
          <span v-if="item.rate_multiplier != null" class="text-primary-600 dark:text-primary-400">{{ t('channelMonitorV2.observation.multiplier', { value: item.rate_multiplier }) }}</span>
          <span v-else>{{ t('channelMonitorV2.observation.priceUnavailable') }}</span>
        </div>
        <div class="mt-2 flex flex-wrap items-center gap-1.5">
          <span v-if="item.source || source" class="badge badge-gray text-[10px]">{{ monitorSourceLabel(item.source || source, t, te) }}</span>
          <span v-if="coverage.state !== 'complete'" class="badge badge-warning text-[10px]">{{ t(`channelMonitorV2.observation.coverage.${coverage.state}`) }}</span>
        </div>
      </div>
    </div>
    <ObservationCurrentStatus class="mt-3" :status="item.current_status" :stale="stale" />
    <div class="mt-4 flex items-center justify-between gap-2"><span class="text-xs text-gray-500">{{ t('channelMonitorV2.unified.traffic') }}</span><ObservationStatus :metrics="item.traffic || item.metrics" :health="item.health" :coverage="coverage" /></div>
    <ObservationMetrics class="my-4" :metrics="item.traffic || item.metrics" :health="item.health" :unavailable="coverage.state === 'unavailable'" />
    <dl class="mb-4 grid grid-cols-2 gap-3 border-t border-gray-100 pt-3 text-xs dark:border-dark-700">
      <div><dt class="text-gray-500">{{ t('channelMonitorV2.unified.probe') }}</dt><dd class="mt-1 text-gray-700 dark:text-gray-200">{{ statusLabel(item.probe?.status) }}</dd></div>
      <div><dt class="text-gray-500">{{ t('channelMonitorV2.unified.quota') }}</dt><dd class="mt-1 text-gray-700 dark:text-gray-200">{{ statusLabel(item.quota?.status) }}</dd></div>
    </dl>
    <dl v-if="admin" class="mb-4 grid grid-cols-2 gap-x-3 gap-y-2 border-y border-gray-100 py-3 text-xs sm:grid-cols-3 dark:border-dark-700">
      <div><dt class="text-gray-400">{{ t('channelMonitorV2.observation.requests') }}</dt><dd class="mt-1 font-semibold tabular-nums text-gray-700 dark:text-gray-200">{{ trafficCount(metrics.request_count) }}</dd></div>
      <div><dt class="text-gray-400">{{ t('channelMonitorV2.observation.errors') }}</dt><dd class="mt-1 font-semibold tabular-nums text-red-600 dark:text-red-400">{{ trafficCount(metrics.channel_errors) }}</dd></div>
      <div><dt class="text-gray-400">{{ t('channelMonitorV2.observation.attempts') }}</dt><dd class="mt-1 font-semibold tabular-nums text-gray-700 dark:text-gray-200">{{ trafficCount(metrics.attempt_count) }}</dd></div>
    </dl>
    <section v-if="admin" class="mb-4 space-y-2 text-xs" data-testid="card-data-quality">
      <p class="text-gray-500 dark:text-gray-400">{{ text('聚合延迟', 'Aggregation lag') }} · <span class="tabular-nums">{{ aggregationLag }}</span></p>
      <p v-if="coverage.gap_reasons?.length" class="break-words text-amber-700 dark:text-amber-300">{{ coverage.gap_reasons.map(gapLabel).join(' · ') }}</p>
      <div class="border-t border-gray-100 pt-2 dark:border-dark-700"><p class="text-gray-400">{{ text('最近异常分类', 'Recent error categories') }}</p><p class="mt-1 break-words text-gray-600 dark:text-gray-300">{{ recentErrorLabel }}</p></div>
    </section>
    <div v-if="expanded" class="mb-4 border-t border-gray-100 pt-3 dark:border-dark-700">
      <p class="mb-2 text-xs font-semibold uppercase tracking-wide text-gray-400">{{ t('channelMonitorV2.observation.modelDetails') }}</p>
      <div v-if="item.models.length" class="space-y-2">
        <div v-for="model in item.models" :key="model.model" class="flex items-center justify-between gap-3 rounded-lg bg-gray-50 px-3 py-2 text-xs dark:bg-dark-900/40">
          <span class="min-w-0 truncate font-medium text-gray-700 dark:text-gray-200">{{ model.model }}</span>
          <span class="shrink-0 tabular-nums text-gray-500 dark:text-gray-400">{{ percent(model.metrics.reliability_rate) }} · {{ formatMonitorMs(model.metrics.ttft.p50_ms) }}</span>
        </div>
      </div>
      <p v-else class="text-xs text-gray-400">{{ t('channelMonitorV2.observation.noModels') }}</p>
    </div>
    <div class="mt-auto">
      <div class="mb-2 flex items-center justify-between gap-2 text-xs text-gray-400">
        <span>{{ t('channelMonitorV2.observation.history') }}</span>
        <button type="button" class="inline-flex items-center gap-1 text-primary-600 dark:text-primary-400" :aria-expanded="expanded" @click="toggleExpanded">{{ t('channelMonitorV2.observation.models', { count: item.models.length }) }}<Icon name="chevronRight" size="xs" :class="expanded ? 'rotate-90' : ''" /></button>
      </div>
      <ObservationTimeline :buckets="item.buckets" :coverage="coverage" :admin="admin" @select="$emit('bucket', item, $event)" />
    </div>
  </article>
</template>
<script setup lang="ts">
import { computed, ref } from 'vue'
import { monitorCategoryLabel, monitorSourceLabel } from './monitorLabels'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import { platformBadgeClass } from '@/utils/platformColors'
import { GROUP_PLATFORM_OPTIONS } from '@/constants/platforms'
import { formatMonitorMs, formatMonitorPercent } from './monitorFormat'
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
const expanded = ref(false)
const percent = (value: number | null | undefined) => value == null ? '-' : formatMonitorPercent(value)
const statusLabel = (value?: string) => {
  const key = `channelMonitorV2.unified.states.${value || 'unknown'}`
  return te(key) ? t(key) : value || t('channelMonitorV2.unified.states.unknown')
}
function toggleExpanded() {
  expanded.value = !expanded.value
}
</script>
