<template>
  <div class="relative min-w-0" @mouseleave="clearTooltip">
    <div class="flex min-h-8 gap-[3px] overflow-x-auto py-1" :aria-label="t('channelMonitorV2.observation.history')" @scroll="clearTooltip">
      <button
        v-for="slot in slots" :key="slot.start" type="button"
        class="h-6 min-w-[5px] flex-1 rounded-sm focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary-500"
        :class="bucketClass(slot.bucket)"
        :aria-describedby="tooltip?.slotStart === slot.start ? tooltipId : undefined"
        :aria-label="tooltipLabel(slot.start, slot.bucket)" :title="tooltipLabel(slot.start, slot.bucket)"
        @mouseenter="showTooltip($event, slot)" @focus="showTooltip($event, slot)"
        @mouseleave="clearTooltip" @blur="clearTooltip"
        @click="$emit('select', slot)"
      />
    </div>
    <div
      v-if="tooltip"
      :id="tooltipId"
      role="tooltip"
      class="pointer-events-none fixed z-[100] max-w-80 -translate-x-1/2 rounded-lg bg-dark-900 px-2.5 py-1.5 text-[11px] leading-4 text-white shadow-lg"
      :class="tooltip.above ? '-translate-y-full' : ''"
      :style="{ left: `${tooltip.left}px`, top: `${tooltip.top}px` }"
    >
      {{ tooltip.text }}
    </div>
    <div class="mt-1 flex justify-between gap-2 text-[10px] tabular-nums text-gray-400">
      <span>{{ time(coverage.requested_start) }}</span><span>{{ time(coverage.requested_end || coverage.data_through) }}</span>
    </div>
  </div>
</template>
<script setup lang="ts">
import { computed, ref, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ObservationBucket, ObservationOverview } from '@/api/channelMonitorV2'
import { observationTimeline } from './observationViewModel'
import { formatMonitorMs, formatMonitorPercent } from './monitorFormat'
const props = withDefaults(defineProps<{ buckets: ObservationBucket[]; coverage: ObservationOverview['coverage']; admin?: boolean }>(), { admin: false })
defineEmits<{ select: [slot: { start: string; bucket: ObservationBucket | null }] }>()
const { t, locale } = useI18n()
const slots = computed(() => observationTimeline(props.buckets, props.coverage))
const tooltipId = `observation-timeline-tooltip-${useId()}`
const tooltip = ref<{ text: string; slotStart: string; left: number; top: number; above: boolean } | null>(null)
const percent = (value: number | null | undefined) => value == null ? '-' : formatMonitorPercent(value)
function time(value: string) {
  const date = new Date(value)
  return Number.isFinite(date.getTime()) ? date.toLocaleString(locale.value, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }) : '-'
}
function period(start: string) {
  const startMs = Date.parse(start)
  const seconds = Number(props.coverage.bucket_seconds)
  if (!Number.isFinite(startMs) || !Number.isFinite(seconds) || seconds <= 0) return time(start)
  const requestedEndMs = Date.parse(props.coverage.requested_end || props.coverage.data_through)
  const calculatedEndMs = startMs + seconds * 1000
  const endMs = Number.isFinite(requestedEndMs) ? Math.min(calculatedEndMs, requestedEndMs) : calculatedEndMs
  if (!(endMs > startMs)) return time(start)
  return `${time(start)} – ${time(new Date(endMs).toISOString())}`
}
function bucketClass(bucket: ObservationBucket | null) {
  if (!bucket || props.coverage.state === 'unavailable') return 'border border-dashed border-gray-300 bg-transparent dark:border-dark-500'
  if (bucket.metrics.sample_state === 'no_samples') return 'bg-gray-200 dark:bg-dark-600'
  if (bucket.metrics.sample_state === 'insufficient') return 'bg-gray-400 dark:bg-dark-400'
  if (bucket.health.reliability === 'critical') return 'bg-red-500'
  if (bucket.health.reliability === 'warning' || bucket.health.latency === 'critical' || bucket.health.latency === 'warning') return 'bg-amber-500'
  if (bucket.health.reliability === 'healthy') return 'bg-emerald-500'
  return 'bg-gray-300 dark:bg-dark-500'
}
function label(start: string, bucket: ObservationBucket | null) {
  const status = bucket ? bucket.metrics.sample_state === 'sufficient' ? bucket.health.reliability : bucket.metrics.sample_state : 'missing'
  return `${period(start)} · ${t(`channelMonitorV2.observation.states.${status}`)}${bucket ? ` · ${t('channelMonitorV2.observation.reliability')} ${percent(bucket.metrics.reliability_rate)}` : ''}`
}
function tooltipLabel(start: string, bucket: ObservationBucket | null) {
  const base = label(start, bucket)
  if (!bucket) return base
  const m = bucket.metrics
  const details = `${base} · ${t('channelMonitorV2.metrics.ttftValue', { value: formatMonitorMs(m.ttft?.p50_ms) })} · ${t('channelMonitorV2.metrics.cacheRateValue', { value: percent(m.cache_rate) })}`
  if (!props.admin) return details
  return `${details} · ${t('channelMonitorV2.observation.requests')} ${m.request_count ?? 0} · ${t('channelMonitorV2.observation.errors')} ${m.channel_errors ?? 0} · ${t('channelMonitorV2.observation.attempts')} ${m.attempt_count ?? 0}`
}
function showTooltip(event: MouseEvent | FocusEvent, slot: { start: string; bucket: ObservationBucket | null }) {
  const target = event.currentTarget as HTMLElement | null
  if (!target) return
  const rect = target.getBoundingClientRect()
  const above = rect.top > 96
  const halfWidth = 144
  const center = Math.min(window.innerWidth - halfWidth, Math.max(halfWidth, rect.left + rect.width / 2))
  tooltip.value = { text: tooltipLabel(slot.start, slot.bucket), slotStart: slot.start, left: center, top: above ? rect.top - 8 : rect.bottom + 8, above }
}
function clearTooltip() {
  tooltip.value = null
}
</script>
