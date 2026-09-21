<template>
  <dl class="grid grid-cols-3 divide-x divide-gray-100 dark:divide-dark-700">
    <div v-for="metric in values" :key="metric.key" class="min-w-0 px-3 first:pl-0 last:pr-0">
      <dt class="text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t(`channelMonitorV2.observation.${metric.key}`) }}</dt>
      <dd class="mt-1 break-words text-lg font-semibold tabular-nums" :class="metric.tone">{{ metric.value }}</dd>
      <dd v-if="metric.detail" class="mt-1 break-words text-[11px] tabular-nums text-gray-400">{{ metric.detail }}</dd>
    </div>
  </dl>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ObservationHealth, ObservationMetrics } from '@/api/channelMonitorV2'
import { formatMonitorMs, formatMonitorPercent } from './monitorFormat'
const props = defineProps<{ metrics: ObservationMetrics; health: ObservationHealth; unavailable?: boolean }>()
const { t } = useI18n()
const tone = (state: string) => state === 'critical' ? 'text-red-600 dark:text-red-400' : state === 'warning' ? 'text-amber-600 dark:text-amber-400' : state === 'healthy' ? 'text-emerald-600 dark:text-emerald-400' : 'text-gray-700 dark:text-gray-200'
const percent = (value: number | null | undefined) => value == null ? '-' : formatMonitorPercent(value)
const values = computed(() => [
  { key: 'reliability', value: props.unavailable ? '-' : percent(props.metrics.reliability_rate), tone: tone(props.unavailable ? 'unknown' : props.health.reliability) },
  { key: 'firstOutput', value: props.unavailable ? '-' : formatMonitorMs(props.metrics.ttft.p50_ms), detail: props.unavailable ? '' : `P95 ${formatMonitorMs(props.metrics.ttft.p95_ms)}`, tone: tone(props.unavailable ? 'unknown' : props.health.latency) },
  { key: 'cache', value: props.unavailable ? '-' : percent(props.metrics.cache_rate), tone: 'text-gray-900 dark:text-gray-100' },
])
</script>
