<template>
  <section class="min-w-0 rounded-2xl border border-gray-200 bg-white p-4 shadow-sm dark:border-dark-700 dark:bg-dark-800 sm:p-5">
    <header class="mb-5 flex flex-wrap items-start justify-between gap-3">
      <div>
        <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ text('渠道趋势', 'Channel trend') }}</h3>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ text('逐渠道查看历史；缺失数据保留断点，不合并不同来源。', 'Channel history; missing data remains a gap. Sources are never combined.') }}</p>
      </div>
      <select v-if="items.length" v-model="selectedKey" class="input !w-auto max-w-full !py-1.5 text-xs" :aria-label="text('选择渠道', 'Choose channel')">
        <option v-for="item in items" :key="key(item)" :value="key(item)">{{ item.group_name }} · {{ platformLabel(item.platform) }}</option>
      </select>
    </header>
    <p v-if="!selected" class="py-16 text-center text-sm text-gray-500">{{ text('当前筛选范围没有趋势数据', 'No trend data for these filters') }}</p>
    <template v-else>
      <div class="mb-4 flex flex-wrap items-center gap-2 text-xs text-gray-500">
        <span class="badge badge-gray">{{ monitorSourceLabel(selected.source || source, t, te) }}</span>
        <span>{{ text('每个点对应一个时间桶，未使用平滑或插值', 'Each point is a time bucket; no smoothing or interpolation') }}</span>
      </div>
      <div class="grid gap-4 sm:grid-cols-2">
        <div v-for="chart in charts" :key="chart.key" class="min-w-0 rounded-xl border border-gray-100 p-3 dark:border-dark-700">
          <div class="flex items-center justify-between gap-2"><h4 class="text-xs font-medium text-gray-600 dark:text-gray-300">{{ chart.label }}</h4><span class="text-sm font-semibold tabular-nums" :style="{ color: chart.color }">{{ chart.value }}</span></div>
          <div class="mt-3 h-40"><Line v-if="slots.length" :data="chart.data" :options="chart.options" /><p v-else class="flex h-full items-center justify-center text-xs text-gray-400">{{ text('暂无时间序列', 'No time series') }}</p></div>
        </div>
      </div>
    </template>
  </section>
</template>
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Chart as ChartJS, CategoryScale, LinearScale, PointElement, LineElement, Tooltip, Legend } from 'chart.js'
import { Line } from 'vue-chartjs'
import type { ObservationChannel, ObservationMetrics, ObservationOverview } from '@/api/channelMonitorV2'
import { formatMonitorMs, formatMonitorPercent } from './monitorFormat'
import { observationTimeline } from './observationViewModel'
import { monitorSourceLabel } from './monitorLabels'
ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, Tooltip, Legend)
const props = defineProps<{ items: ObservationChannel[]; coverage?: ObservationOverview['coverage']; source?: ObservationOverview['source'] }>()
const { t, te, locale } = useI18n()
const text = (zh: string, en: string) => locale.value.startsWith('zh') ? zh : en
const platformLabel = (value: string) => { const key = `channelMonitorV2.platforms.${value}`; return te(key) ? t(key) : value }
const key = (item: ObservationChannel) => `${item.platform}:${item.group_id}`
const selectedKey = ref('')
watch(() => props.items, items => { if (!items.some(item => key(item) === selectedKey.value)) selectedKey.value = items[0] ? key(items[0]) : '' }, { immediate: true })
const selected = computed(() => props.items.find(item => key(item) === selectedKey.value))
const slots = computed(() => {
  if (!selected.value || props.coverage?.state === 'unavailable') return []
  return props.coverage ? observationTimeline(selected.value.buckets, props.coverage) : [...selected.value.buckets].sort((a, b) => Date.parse(a.bucket_start) - Date.parse(b.bucket_start)).map(bucket => ({ start: bucket.bucket_start, bucket }))
})
const percent = (value: number | null | undefined) => value == null ? '—' : formatMonitorPercent(value)
// Reliability excludes client/cancelled outcomes; keep that same denominator in
// its complement instead of pretending to have a separate all-errors rate.
const channelErrorRate = (metrics?: ObservationMetrics) => metrics?.reliability_rate == null ? null : 1 - metrics.reliability_rate
const charts = computed(() => {
  const m = selected.value?.traffic || selected.value?.metrics
  const labels = slots.value.map(slot => new Date(slot.start).toLocaleString(locale.value, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }))
  const series = (read: (value: ObservationMetrics) => number | null | undefined, percentValue = false) => slots.value.map(slot => {
    const value = slot.bucket?.metrics.sample_state !== 'no_samples' && slot.bucket ? read(slot.bucket.metrics) : null
    return value == null ? null : percentValue ? value * 100 : value
  })
  const entries = [
    { key: 'reliability', label: text('可靠性', 'Reliability'), value: percent(m?.reliability_rate), color: '#10b981', percent: true, datasets: [{ label: text('可靠性', 'Reliability'), data: series(value => value.reliability_rate, true), borderColor: '#10b981' }] },
    { key: 'ttft', label: text('首字延迟 P50 / P95', 'TTFT P50 / P95'), value: `${formatMonitorMs(m?.ttft?.p50_ms)} / ${formatMonitorMs(m?.ttft?.p95_ms)}`, color: '#0ea5e9', percent: false, datasets: [{ label: 'P50', data: series(value => value.ttft?.p50_ms), borderColor: '#0ea5e9' }, { label: 'P95', data: series(value => value.ttft?.p95_ms), borderColor: '#8b5cf6' }] },
    { key: 'errors', label: text('渠道错误率', 'Channel error rate'), value: percent(channelErrorRate(m)), color: '#f43f5e', percent: true, datasets: [{ label: text('渠道错误率（1 − 可靠性）', 'Channel errors (1 − reliability)'), data: series(channelErrorRate, true), borderColor: '#f43f5e' }] },
    { key: 'cache', label: text('缓存率', 'Cache rate'), value: percent(m?.cache_rate), color: '#6366f1', percent: true, datasets: [{ label: text('缓存率', 'Cache rate'), data: series(value => value.cache_rate, true), borderColor: '#6366f1' }] },
  ]
  return entries.map(entry => ({ ...entry, data: { labels, datasets: entry.datasets.map(dataset => ({ ...dataset, borderWidth: 2, pointRadius: 0, pointHitRadius: 10, tension: 0, spanGaps: false })) }, options: { responsive: true, maintainAspectRatio: false, animation: false as const, interaction: { mode: 'index' as const, intersect: false }, plugins: { legend: { display: entry.datasets.length > 1, labels: { color: '#94a3b8', boxWidth: 12, font: { size: 10 } } }, tooltip: { callbacks: { label: (ctx: { dataset: { label?: string }; parsed: { y: number | null } }) => `${ctx.dataset.label || ''}: ${ctx.parsed.y == null ? '—' : entry.percent ? `${ctx.parsed.y.toFixed(1)}%` : formatMonitorMs(ctx.parsed.y)}` } } }, scales: { x: { ticks: { maxTicksLimit: 4, maxRotation: 0, color: '#94a3b8', font: { size: 9 } }, grid: { display: false } }, y: { min: 0, ...(entry.percent ? { max: 100 } : {}), ticks: { color: '#94a3b8', font: { size: 9 } }, grid: { color: 'rgba(148,163,184,0.10)' } } } } }))
})
</script>
