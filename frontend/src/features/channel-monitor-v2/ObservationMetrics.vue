<template>
  <dl class="grid grid-cols-3" :class="variant === 'card' ? ['card-metrics gap-2', { 'metrics-comfortable': preferences.density === 'comfortable' }] : 'divide-x divide-gray-100 dark:divide-dark-700'">
    <div v-for="metric in values" :key="metric.key" class="min-w-0" :class="variant === 'card' ? 'card-metric-cell rounded-xl border border-gray-100 bg-gray-50/80 py-2.5 dark:border-dark-700 dark:bg-dark-900/40' : 'px-3 first:pl-0 last:pr-0'">
      <dt class="text-xs text-gray-500 dark:text-gray-400" :class="variant === 'card' ? 'leading-4' : 'leading-5'">{{ t(`channelMonitorV2.observation.${metric.key}`) }}</dt>
      <dd class="mt-1 break-words font-semibold tabular-nums" :class="[variant === 'card' ? 'card-metric-value font-mono leading-6' : 'text-lg', metric.tone]">{{ metric.value }}</dd>
      <dd v-if="metric.detail && (variant !== 'card' || preferences.metricDetails)" class="mt-1 break-words text-[11px] tabular-nums text-gray-400">{{ metric.detail }}</dd>
    </div>
  </dl>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import { useMonitorPreferences } from './useMonitorPreferences'
import { useI18n } from 'vue-i18n'
import type { ObservationHealth, ObservationMetrics } from '@/api/channelMonitorV2'
import { formatMonitorMs, formatMonitorPercent } from './monitorFormat'
const props = withDefaults(defineProps<{ metrics: ObservationMetrics; health: ObservationHealth; unavailable?: boolean; variant?: 'default' | 'card' }>(), { variant: 'default' })
const { t } = useI18n()
const preferences = useMonitorPreferences()
const tone = (state: string) => state === 'critical' ? 'text-red-600 dark:text-red-400' : state === 'warning' ? 'text-amber-600 dark:text-amber-400' : state === 'healthy' ? 'text-emerald-600 dark:text-emerald-400' : 'text-gray-700 dark:text-gray-200'
const percent = (value: number | null | undefined) => value == null ? '-' : formatMonitorPercent(value)
const values = computed(() => {
  const metrics = [
    { key: 'reliability', value: props.unavailable ? '-' : percent(props.metrics.reliability_rate), tone: tone(props.unavailable ? 'unknown' : props.health.reliability) },
    { key: 'firstOutput', value: props.unavailable ? '-' : formatMonitorMs(props.metrics.ttft.p50_ms), detail: props.unavailable ? '' : `P95 ${formatMonitorMs(props.metrics.ttft.p95_ms)}`, tone: tone(props.unavailable ? 'unknown' : props.health.latency) },
    { key: 'cache', value: props.unavailable ? '-' : percent(props.metrics.cache_rate), tone: 'text-gray-900 dark:text-gray-100' },
  ]
  return props.variant === 'card' ? [metrics[2], metrics[0], metrics[1]] : metrics
})
</script>

<style scoped>
.card-metrics { container-type: inline-size; }
.card-metric-cell { padding-inline: clamp(8px, 2.7cqi, 12px); }
.metrics-comfortable .card-metric-cell { padding-block: 16px; }
.card-metric-value { font-size: clamp(17px, 5.4cqi, 20px); }
</style>
