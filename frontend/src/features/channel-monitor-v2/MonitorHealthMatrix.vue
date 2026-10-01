<template>
  <section class="min-w-0 rounded-2xl border border-gray-200 bg-white p-4 shadow-sm dark:border-dark-700 dark:bg-dark-800 sm:p-5">
    <header class="mb-4 flex flex-wrap items-start justify-between gap-3">
      <div><h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ text('流量状态矩阵', 'Traffic health matrix') }}</h3><p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ text('仅反映真实请求；探针和配额不会改变颜色。', 'Real traffic only. Probes and quota do not change these colors.') }}</p></div>
      <div class="flex items-center gap-2"><span class="text-xs text-gray-400">{{ rows.length }} {{ text('个渠道', 'channels') }}</span><select v-model="axis" class="input !w-auto !py-1.5 text-xs" :aria-label="text('矩阵维度', 'Matrix dimension')"><option value="model">{{ text('按模型', 'By model') }}</option><option v-if="coverage" value="time">{{ text('按时间', 'By time') }}</option></select></div>
    </header>
    <div v-if="!rows.length" class="py-12 text-center text-sm text-gray-500">{{ text('当前范围没有矩阵数据', 'No matrix data for this range') }}</div>
    <div v-else class="overflow-x-auto">
      <table class="w-full min-w-[600px] text-left text-xs">
        <thead><tr class="border-b border-gray-100 text-gray-400 dark:border-dark-700"><th scope="col" class="pb-3 pr-3 font-medium">{{ text('平台 / 渠道', 'Platform / channel') }}</th><th v-for="column in columns" :key="column" scope="col" class="max-w-36 px-2 pb-3 text-center font-medium"><span class="inline-block max-w-36 break-words">{{ columnLabel(column) }}</span></th><th scope="col" class="pb-3 pl-2 text-right font-medium">{{ text('可靠性', 'Reliability') }}</th></tr></thead>
        <tbody><tr v-for="row in rows" :key="`${row.platform}:${row.group_id}`" class="border-b border-gray-100 last:border-0 dark:border-dark-700/60">
          <th scope="row" class="py-4 pr-3 font-normal"><button class="text-left font-semibold text-gray-800 hover:text-primary-600 dark:text-gray-100" type="button" @click="emit('detail', row)">{{ row.group_name || platformLabel(row.platform) }}</button><div class="mt-1 text-[10px] text-gray-400">{{ platformLabel(row.platform) }} · {{ monitorSourceLabel(row.source || source, t, te) }}</div></th>
          <td v-for="column in columns" :key="column" class="px-2 py-3 text-center"><button type="button" class="mx-auto flex h-8 w-10 items-center justify-center rounded-md text-xs font-bold focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary-500" :class="cellClass(cell(row, column))" :aria-label="cellTitle(row, column)" :title="cellTitle(row, column)" @click="selectCell(row, column)">{{ symbol(cellState(cell(row, column))) }}</button></td>
          <td class="py-3 pl-2 text-right font-semibold tabular-nums text-gray-700 dark:text-gray-200">{{ coverage?.state === 'unavailable' ? '—' : percent((row.traffic || row.metrics).reliability_rate) }}</td>
        </tr></tbody>
      </table>
    </div>
    <footer class="mt-4 flex flex-wrap gap-x-4 gap-y-2 text-[11px] text-gray-500 dark:text-gray-400"><span v-for="state in states" :key="state" class="inline-flex items-center gap-1.5"><span class="flex h-4 w-4 items-center justify-center rounded-sm text-[9px]" :class="classes[state]">{{ symbol(state) }}</span>{{ label(state) }}</span></footer>
  </section>
</template>
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { monitorSourceLabel } from './monitorLabels'
import { formatMonitorPercent } from './monitorFormat'
import { observationTimeline } from './observationViewModel'
import type { ObservationBucket, ObservationChannel, ObservationModel, ObservationOverview } from '@/api/channelMonitorV2'
const props = defineProps<{ items: ObservationChannel[]; coverage?: ObservationOverview['coverage']; source?: ObservationOverview['source'] }>()
const emit = defineEmits<{ detail: [item: ObservationChannel]; model: [item: ObservationChannel, model: string]; bucket: [item: ObservationChannel, slot: { start: string; bucket: ObservationBucket | null }] }>()
const { t, te, locale } = useI18n()
const text = (zh: string, en: string) => locale.value.startsWith('zh') ? zh : en
const axis = ref<'model' | 'time'>('model')
const rows = computed(() => props.items)
const columns = computed(() => axis.value === 'time' && props.coverage ? observationTimeline([], props.coverage).map(slot => slot.start) : [...new Set(rows.value.flatMap(row => row.models.map(model => model.model)))])
const states = ['healthy', 'warning', 'critical', 'no_samples', 'insufficient', 'missing', 'unknown'] as const
type CellState = typeof states[number]
type Cell = ObservationModel | ObservationBucket | undefined
const classes: Record<CellState, string> = { healthy: 'bg-emerald-500 text-white', warning: 'bg-amber-400 text-amber-950', critical: 'bg-red-500 text-white', no_samples: 'bg-gray-200 text-gray-500 dark:bg-dark-600 dark:text-gray-300', insufficient: 'border border-amber-400 bg-amber-50 text-amber-700 dark:bg-amber-950/30 dark:text-amber-300', missing: 'border border-dashed border-gray-400 text-gray-400', unknown: 'bg-gray-400 text-white dark:bg-dark-500' }
function cell(row: ObservationChannel, column: string): Cell { return axis.value === 'time' ? row.buckets.find(bucket => Date.parse(bucket.bucket_start) === Date.parse(column)) : row.models.find(model => model.model === column) }
function cellState(value: Cell): CellState {
  if (!value || props.coverage?.state === 'unavailable') return 'missing'
  const metrics = 'traffic' in value && value.traffic ? value.traffic : value.metrics
  if (metrics.sample_state === 'no_samples' || metrics.sample_state === 'insufficient') return metrics.sample_state
  if (value.health.reliability === 'critical' || value.health.latency === 'critical') return 'critical'
  if (value.health.reliability === 'warning' || value.health.latency === 'warning') return 'warning'
  return value.health.reliability === 'healthy' && value.health.latency === 'healthy' ? 'healthy' : 'unknown'
}
function cellClass(value: Cell) { return classes[cellState(value)] }
function symbol(state: CellState) { return ({ healthy: '✓', warning: '!', critical: '×', no_samples: '–', insufficient: '~', missing: '', unknown: '?' })[state] }
function label(state: CellState) { return t(`channelMonitorV2.observation.states.${state}`) }
function platformLabel(value: string) { const key = `channelMonitorV2.platforms.${value}`; return te(key) ? t(key) : value }
function columnLabel(value: string) { return axis.value === 'time' ? new Date(value).toLocaleString(locale.value, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }) : value }
function cellTitle(row: ObservationChannel, column: string) { const value = cell(row, column); return `${row.group_name} · ${columnLabel(column)} · ${label(cellState(value))}${value ? ` · ${percent(value.metrics.reliability_rate)}` : ''}` }
function percent(value: number | null | undefined) { return value == null ? '—' : formatMonitorPercent(value) }
function selectCell(row: ObservationChannel, column: string) {
  if (axis.value === 'time') emit('bucket', row, { start: column, bucket: (cell(row, column) as ObservationBucket | undefined) || null })
  else if (cell(row, column)) emit('model', row, column)
}
</script>
