<template>
  <div class="trend-plot relative h-full min-w-0" :class="{ 'trend-motion-off': !motionEnabled }" @mouseleave="hideHover" @touchend="hideHover" @touchcancel="hideHover">
    <Line ref="line" v-bind="$attrs" :data="styledData" :options="styledOptions" :plugins="chartPlugins" />
    <div v-if="hover" class="trend-hover pointer-events-none absolute inset-0" :class="{ 'is-visible': hover.visible }" aria-hidden="true">
      <div class="trend-crosshair absolute left-0 w-px bg-slate-300/80 dark:bg-slate-500/70" :class="{ 'is-continuous': hover.continuous }" :style="{ transform: `translateX(${hover.x}px)`, top: `${hover.top}px`, height: `${hover.height}px` }" />
      <span v-for="point in hover.points" :key="point.index" class="trend-point absolute left-0 top-0 h-2.5 w-2.5 rounded-full border-2 border-white dark:border-dark-800" :class="{ 'is-continuous': point.continuous }" :style="{ transform: `translate(${point.x - 5}px, ${point.y - 5}px)`, backgroundColor: point.color, boxShadow: `0 0 0 4px ${alpha(point.color, 0.12)}` }" />
      <div ref="tooltipElement" class="trend-tooltip absolute left-0 top-0 max-w-full overflow-hidden rounded-xl border border-slate-200/80 bg-white/95 text-xs shadow-lg shadow-slate-900/10 backdrop-blur-sm dark:border-dark-600 dark:bg-dark-800/95" :style="{ transform: `translate(${hover.left}px, ${hover.y}px)`, width: `${hover.width}px` }">
        <div v-if="hover.title.length" class="border-b border-slate-100 px-3 py-2 text-[11px] text-slate-500 dark:border-dark-700 dark:text-slate-400"><div v-for="(title, index) in hover.title" :key="index">{{ title }}</div></div>
        <div class="px-3 py-2.5" :class="hover.columns ? 'grid grid-cols-2 gap-x-3 gap-y-1 text-[11px] leading-[14px]' : 'space-y-2'">
          <div v-for="(text, index) in hover.before" :key="`before-${index}`" class="text-slate-500 dark:text-slate-400">{{ text }}</div>
          <div v-for="(row, index) in hover.rows" :key="index" class="flex min-w-0 items-start gap-2">
            <span class="mt-1.5 h-0.5 w-3 shrink-0 rounded-full" :style="{ backgroundColor: row.color }" />
            <div class="min-w-0 flex-1 space-y-1">
              <div v-for="(text, textIndex) in row.lines" :key="textIndex" class="flex justify-between gap-3 break-words text-slate-600 dark:text-slate-300">
                <template v-if="text.startsWith(`${row.label}: `)"><span class="min-w-0">{{ row.label }}</span><span class="shrink-0 font-medium tabular-nums text-slate-900 dark:text-slate-100">{{ text.slice(row.label.length + 2) }}</span></template>
                <span v-else>{{ text }}</span>
              </div>
            </div>
          </div>
          <div v-for="(text, index) in hover.after" :key="`after-${index}`" class="text-slate-500 dark:text-slate-400">{{ text }}</div>
        </div>
        <div v-if="hover.footer.length" class="space-y-1 border-t border-slate-100 px-3 py-2 text-[11px] text-slate-500 dark:border-dark-700 dark:text-slate-400"><div v-for="(text, index) in hover.footer" :key="index">{{ text }}</div></div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { usePreferredReducedMotion } from '@vueuse/core'
import { Line } from 'vue-chartjs'
import { Chart as ChartJS, CategoryScale, LinearScale, PointElement, LineElement, Tooltip, Legend, Filler, type ChartData, type ChartOptions, type Plugin, type ScriptableContext, type TooltipModel } from 'chart.js'
import { color } from 'chart.js/helpers'

defineOptions({ inheritAttrs: false })
ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, Tooltip, Legend, Filler)
const props = withDefaults(defineProps<{
  data: ChartData<'line'>
  options?: ChartOptions<'line'>
  plugins?: Plugin<'line'>[]
  motion?: boolean
}>(), { motion: true, plugins: () => [] })
const line = ref<{ chart?: ChartJS<'line'> } | null>(null)
defineExpose({ chart: computed(() => line.value?.chart) })
const reducedMotion = usePreferredReducedMotion()
const motionEnabled = computed(() => props.motion && reducedMotion.value !== 'reduce')
const palette = ['#10b981', '#0ea5e9', '#8b5cf6', '#f59e0b', '#f43f5e']
const alpha = (value: string, opacity: number) => color(value).alpha(opacity).rgbString()
function seriesColor(index: number) {
  const value = props.data.datasets[index]?.borderColor
  return typeof value === 'string' ? value : palette[index % palette.length]
}
function areaFill(context: ScriptableContext<'line'>) {
  const value = seriesColor(context.datasetIndex)
  const { ctx, chartArea } = context.chart
  if (!chartArea) return alpha(value, 0.08)
  const gradient = ctx.createLinearGradient(0, chartArea.top, 0, chartArea.bottom)
  gradient.addColorStop(0, alpha(value, 0.17))
  gradient.addColorStop(1, alpha(value, 0))
  return gradient
}
const styledData = computed<ChartData<'line'>>(() => ({
  ...props.data,
  datasets: props.data.datasets.map((dataset, index) => ({
    ...dataset,
    borderColor: dataset.borderColor || seriesColor(index),
    borderWidth: 2,
    borderCapStyle: 'round',
    borderJoinStyle: 'round',
    pointRadius: (context: ScriptableContext<'line'>) => {
      const values = dataset.data
      const i = context.dataIndex
      return values[i] != null && values[i - 1] == null && values[i + 1] == null ? 3 : 0
    },
    pointHoverRadius: 0,
    pointHitRadius: 10,
    // Only interpolate the stroke. Never average samples or bridge null buckets.
    cubicInterpolationMode: 'monotone',
    spanGaps: false,
    fill: dataset.fill === undefined || dataset.fill === true ? 'origin' : dataset.fill,
    backgroundColor: areaFill,
  })),
}))

interface HoverState {
  visible: boolean
  continuous: boolean
  x: number
  top: number
  height: number
  left: number
  y: number
  width: number
  columns: boolean
  title: string[]
  before: string[]
  after: string[]
  footer: string[]
  rows: { color: string; label: string; lines: string[] }[]
  points: { index: number; x: number; y: number; color: string; continuous: boolean }[]
}
const hover = ref<HoverState | null>(null)
const tooltipElement = ref<HTMLElement | null>(null)
let hoverVersion = 0
let cursorX: number | null = null
function hideHover() {
  cursorX = null
  if (hover.value) hover.value.visible = false
}
function markerPosition(chart: ChartJS<'line'>, point: TooltipModel<'line'>['dataPoints'][number]) {
  const sample = point.element.getProps(['x', 'y'], true)
  const dataset = props.data.datasets[point.datasetIndex]
  // Sparse charts retain sample-based hover: never invent points in a gap.
  if (cursorX !== null && dataset.data.length > 1 && dataset.data.every(value => value != null)) {
    const meta = chart.getDatasetMeta(point.datasetIndex)
    if (meta.dataset instanceof LineElement && meta.data.every(element => !(element as PointElement).skip)) {
      const interpolated = meta.dataset.interpolate({ x: cursorX, y: 0 }, 'x')
      const position = Array.isArray(interpolated) ? interpolated[0] : interpolated
      if (position && Number.isFinite(position.x) && Number.isFinite(position.y)) {
        return { ...position, continuous: true }
      }
    }
  }
  return { x: sample.x, y: sample.y, continuous: false }
}
function updateHover({ chart, tooltip }: { chart: ChartJS<'line'>; tooltip: TooltipModel<'line'> }) {
  if (!tooltip.opacity || !tooltip.dataPoints?.length) { hideHover(); return }
  const points = tooltip.dataPoints.filter(point => point.parsed.y != null).map(point => ({
    index: point.datasetIndex,
    ...markerPosition(chart, point),
    color: seriesColor(point.datasetIndex),
  }))
  if (!points.length) { hideHover(); return }
  const continuous = points.some(point => point.continuous)
  const x = continuous && cursorX !== null ? cursorX : points[0].x
  const tooltipX = cursorX ?? x
  const columns = tooltip.dataPoints.length > 6
  const width = Math.min(columns ? 400 : 224, chart.width)
  const right = tooltipX + 16
  const left = tooltipX - width - 16
  // Keep the cursor clear; only center within bounds when neither side has room.
  const tooltipLeft = right + width <= chart.width ? right : left >= 0 ? left : Math.max(0, Math.min(chart.width - width, tooltipX - width / 2))
  const state: HoverState = {
    visible: true, continuous, x, top: chart.chartArea.top, height: chart.chartArea.height,
    left: tooltipLeft,
    // Preserve measured height placement so layout reads do not restart the transform transition.
    y: hover.value?.y ?? 8, width, columns, title: tooltip.title || [], before: tooltip.beforeBody || [], after: tooltip.afterBody || [], footer: tooltip.footer || [],
    // Chart.js has already applied the caller's label/title/footer callbacks.
    rows: tooltip.body.map((body, index) => ({
      color: seriesColor(tooltip.dataPoints[index].datasetIndex),
      label: tooltip.dataPoints[index].dataset.label || '',
      lines: [...body.before, ...body.lines, ...body.after],
    })),
    points,
  }
  hover.value = state
  const version = ++hoverVersion
  void nextTick(() => {
    if (version !== hoverVersion) return
    if (hover.value && tooltipElement.value) hover.value.y = Math.max(0, Math.min(8, chart.height - tooltipElement.value.offsetHeight))
  })
}
const hoverPlugin: Plugin<'line'> = {
  id: 'trend-continuous-hover',
  afterEvent(chart, { event, inChartArea }) {
    if (event.type === 'mouseout' || !inChartArea || typeof event.x !== 'number') {
      hideHover()
      return
    }
    cursorX = Math.max(chart.chartArea.left, Math.min(chart.chartArea.right, event.x))
    // The tooltip's active date can stay the same while the cursor moves.
    if (chart.tooltip) updateHover({ chart, tooltip: chart.tooltip })
  },
  afterDraw(chart) {
    // Keep markers on the rendered curve while the chart itself animates.
    if (cursorX !== null && chart.tooltip?.opacity) updateHover({ chart, tooltip: chart.tooltip })
  },
}
const chartPlugins = computed(() => [...props.plugins, hoverPlugin])
const styledOptions = computed<ChartOptions<'line'>>(() => {
  const options = props.options || {}
  const inputScales: ChartOptions<'line'>['scales'] = {
    ...(props.data.datasets.some(dataset => !dataset.xAxisID) ? { x: {} } : {}),
    ...(props.data.datasets.some(dataset => !dataset.yAxisID) ? { y: {} } : {}),
    ...options.scales,
  }
  const scales = Object.fromEntries(Object.entries(inputScales).map(([id, scale]) => [id, {
    ...scale,
    border: { ...scale?.border, display: false },
    grid: { ...scale?.grid, color: 'rgba(148,163,184,0.14)', drawTicks: false, ...(id === 'x' || scale?.axis === 'x' ? { display: false } : {}) },
    ticks: { maxRotation: 0, autoSkipPadding: 16, maxTicksLimit: 6, ...scale?.ticks, color: '#94a3b8', padding: 8 },
  }])) as ChartOptions<'line'>['scales']
  return {
    ...options,
    responsive: true,
    maintainAspectRatio: false,
    animation: motionEnabled.value ? { duration: 350, easing: 'easeOutQuart' } : false,
    interaction: { ...options.interaction, mode: 'index', axis: 'x', intersect: false },
    onResize: (chart, size) => { hideHover(); options.onResize?.(chart, size) },
    plugins: {
      ...options.plugins,
      legend: { ...options.plugins?.legend, labels: { ...options.plugins?.legend?.labels, color: '#94a3b8', usePointStyle: true, pointStyle: 'line', boxWidth: 12 } },
      tooltip: { ...options.plugins?.tooltip, enabled: false, animation: false, external: updateHover },
    },
    scales,
  }
})
watch([() => props.data, () => props.options, motionEnabled], () => { cursorX = null; hover.value = null })
</script>

<style scoped>
.trend-hover { opacity: 0; transition: opacity 120ms ease; }
.trend-hover.is-visible { opacity: 1; }
.trend-crosshair, .trend-point, .trend-tooltip { transition: transform 160ms cubic-bezier(0.22, 1, 0.36, 1); }
.trend-crosshair.is-continuous, .trend-point.is-continuous { transition: none; }
.trend-motion-off .trend-hover, .trend-motion-off .trend-crosshair,
.trend-motion-off .trend-point, .trend-motion-off .trend-tooltip { transition: none; }
@media (prefers-reduced-motion: reduce) {
  .trend-hover, .trend-crosshair, .trend-point, .trend-tooltip { transition: none; }
}
</style>
