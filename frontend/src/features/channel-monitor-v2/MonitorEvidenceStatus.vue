<template>
  <div class="flex flex-wrap items-center gap-2 rounded-xl border border-gray-200 bg-white px-3 py-2.5 text-xs dark:border-dark-700 dark:bg-dark-800">
    <span class="inline-flex items-center gap-1.5 font-medium text-gray-700 dark:text-gray-200"><span class="h-2 w-2 rounded-full" :class="collectorClass" />{{ collectorLabel }}</span>
    <span class="text-gray-300 dark:text-dark-600">•</span>
    <span class="badge badge-gray">{{ sourceLabel }}</span>
    <span class="text-gray-500 dark:text-gray-400">{{ coverageLabel }}</span>
    <span v-if="coverageWindow" class="text-gray-500 dark:text-gray-400">{{ coverageWindow }}</span>
    <span class="text-gray-500 dark:text-gray-400">{{ aggregationLag }}</span>
    <span v-if="gapReasons.length" class="text-amber-600 dark:text-amber-400">{{ gapReasons.join(' · ') }}</span>
    <span v-if="updatedAt" class="ml-auto text-gray-400">{{ updatedAt }}</span>
  </div>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ObservationOverview } from '@/api/channelMonitorV2'
import { monitorSourceLabel } from './monitorLabels'
const props = defineProps<{ overview: ObservationOverview; stale?: boolean }>()
const { t, te, locale } = useI18n()
const coverage = computed(() => props.overview.coverage)
const sourceLabel = computed(() => monitorSourceLabel(props.overview.source, t, te))
const collectorLabel = computed(() => {
  if (props.stale) return t('channelMonitorV2.evidence.snapshotStale')
  const key = `channelMonitorV2.unified.collector.${coverage.value.collector_state || 'unknown'}`
  return te(key) ? t(key) : coverage.value.collector_state || t('channelMonitorV2.unified.collector.unknown')
})
const collectorClass = computed(() => props.stale || coverage.value.collector_state === 'stale' || coverage.value.collector_state === 'backlogged' ? 'bg-amber-500' : coverage.value.collector_state === 'write_failed' ? 'bg-red-500' : coverage.value.collector_state === 'healthy' ? 'bg-emerald-500' : 'bg-gray-400')
const coverageLabel = computed(() => t(`channelMonitorV2.observation.coverage.${coverage.value.state}`))
const coverageWindow = computed(() => {
  const start = Date.parse(coverage.value.coverage_start)
  const end = Date.parse(coverage.value.data_through)
  return start > 0 && end >= start
    ? t('channelMonitorV2.evidence.coverageWindow', { start: new Date(start).toLocaleString(locale.value), end: new Date(end).toLocaleString(locale.value) })
    : ''
})
const aggregationLag = computed(() => {
  const seconds = coverage.value.aggregation_lag_seconds
  return Number.isFinite(seconds) && seconds >= 0
    ? t('channelMonitorV2.evidence.aggregationLagValue', { seconds })
    : t('channelMonitorV2.evidence.aggregationLagUnknown')
})
const gapReasons = computed(() => (coverage.value.gap_reasons || []).map(reason => {
  const key = `channelMonitorV2.unified.gaps.${reason}`
  return te(key) ? t(key) : reason
}))
const updatedAt = computed(() => { const value = coverage.value.last_ingested_at || coverage.value.data_through; return value && Date.parse(value) > 0 ? t('channelMonitorV2.updatedTo', { time: new Date(value).toLocaleString(locale.value) }) : '' })
</script>
