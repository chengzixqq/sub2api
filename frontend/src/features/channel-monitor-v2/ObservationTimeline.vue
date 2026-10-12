<template>
  <div class="relative min-w-0" @mouseleave="leaveTimeline" @keydown.esc.stop="clearTooltip">
    <div data-testid="observation-timeline" class="grid min-h-8 w-full py-1" :style="{ gridTemplateColumns: `repeat(${Math.max(1, slots.length)}, minmax(0, 1fr))`, gap: slots.length <= 60 ? variant === 'card' ? '4px' : '2px' : '0' }" :aria-label="t('channelMonitorV2.observation.history')">
      <button
        v-for="slot in slots" :key="slot.start" type="button"
        class="timeline-segment min-w-0 focus-visible:z-10 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary-500"
        :class="[variant === 'card' ? 'h-6 rounded-[5px]' : 'h-6 first:rounded-l-sm last:rounded-r-sm', bucketClass(slot.bucket)]"
        :aria-describedby="tooltip?.slotStart === slot.start ? tooltipId : undefined"
        :aria-label="tooltipLabel(slot.start, slot.bucket)"
        @mouseenter="activateTooltip($event, slot, 'hover')" @focus="activateTooltip($event, slot, 'focus')"
        @mouseleave="leaveTimeline" @blur="blurTooltip"
        @click="clearTooltip(); $emit('select', slot)"
      />
    </div>
    <Teleport to="body">
      <div :key="admin ? 'admin' : 'user'" class="contents">
      <Transition name="timeline-tooltip">
        <div
          v-if="tooltip"
          :id="tooltipId"
          role="tooltip"
          class="timeline-tooltip pointer-events-none fixed z-[100] w-80 max-w-[calc(100vw-1.5rem)] -translate-x-1/2"
          :class="tooltip.above ? 'is-above -translate-y-full' : 'is-below'"
          :style="{ left: `${tooltip.left}px`, top: `${tooltip.top}px`, '--tooltip-arrow': `${tooltip.arrow}px` }"
        >
          <div class="timeline-tooltip-bubble relative rounded-lg bg-dark-900 px-3 py-2 text-[11px] font-medium leading-4 text-white shadow-lg">
            {{ tooltipText }}
          </div>
        </div>
      </Transition>
      </div>
    </Teleport>
    <div class="mt-1 flex justify-between gap-2 text-[10px] tabular-nums text-gray-400">
      <span>{{ time(coverage.requested_start) }}</span><span>{{ time(coverage.requested_end || coverage.data_through) }}</span>
    </div>
  </div>
</template>
<script setup lang="ts">
import { computed, getCurrentInstance, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ObservationBucket, ObservationOverview } from '@/api/channelMonitorV2'
import { observationTimeline } from './observationViewModel'
import { formatMonitorMs, formatMonitorPercent } from './monitorFormat'
const props = withDefaults(defineProps<{ buckets: ObservationBucket[]; coverage: ObservationOverview['coverage']; admin?: boolean; variant?: 'default' | 'card' }>(), { admin: false, variant: 'default' })
defineEmits<{ select: [slot: { start: string; bucket: ObservationBucket | null }] }>()
const { t, locale } = useI18n()
const slots = computed(() => observationTimeline(props.buckets, props.coverage))
let fallbackTimelineId = 0
const tooltipId = `observation-timeline-tooltip-${getCurrentInstance()?.uid ?? ++fallbackTimelineId}`
type ActiveSlot = { target: HTMLElement; start: string }
let hoveredSlot: ActiveSlot | null = null
let focusedSlot: ActiveSlot | null = null
const tooltip = ref<{ slotStart: string; left: number; top: number; above: boolean; arrow: number } | null>(null)
const lastSlotStart = ref('')
const tooltipText = computed(() => {
  const slot = slots.value.find((candidate) => candidate.start === lastSlotStart.value)
  return slot ? tooltipLabel(slot.start, slot.bucket) : ''
})
watch(slots, (next) => {
  if (tooltip.value && !next.some((slot) => slot.start === tooltip.value?.slotStart)) clearTooltip()
})
const percent = (value: number | null | undefined) => value == null ? '-' : formatMonitorPercent(value)
function time(value: string, precise = false) {
  const date = new Date(value)
  return Number.isFinite(date.getTime()) ? date.toLocaleString(locale.value, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', ...(precise ? { second: '2-digit' } : {}) }) : '-'
}
function period(start: string) {
  const startMs = Date.parse(start)
  const seconds = Number(props.coverage.bucket_seconds)
  if (!Number.isFinite(startMs) || !Number.isFinite(seconds) || seconds <= 0) return time(start)
  const requestedStartMs = Date.parse(props.coverage.requested_start)
  const requestedEndMs = Date.parse(props.coverage.requested_end || props.coverage.data_through)
  const effectiveStartMs = Number.isFinite(requestedStartMs) ? Math.max(startMs, requestedStartMs) : startMs
  const calculatedEndMs = startMs + seconds * 1000
  const endMs = Number.isFinite(requestedEndMs) ? Math.min(calculatedEndMs, requestedEndMs) : calculatedEndMs
  if (!(endMs > effectiveStartMs)) return time(start)
  const clipped = effectiveStartMs !== startMs || endMs !== calculatedEndMs
  return `${time(new Date(effectiveStartMs).toISOString(), clipped)} – ${time(new Date(endMs).toISOString(), clipped)}`
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
  const details = `${base} · ${t('channelMonitorV2.metrics.ttftValue', { value: formatMonitorMs(m.ttft?.p50_ms) })} · ${t('channelMonitorV2.metrics.durationValue', { value: formatMonitorMs(m.duration?.p50_ms) })} · ${t('channelMonitorV2.metrics.cacheRateValue', { value: percent(m.cache_rate) })}`
  if (!props.admin) return details
  return `${details} · ${t('channelMonitorV2.observation.requests')} ${m.request_count ?? 0} · ${t('channelMonitorV2.observation.errors')} ${m.channel_errors ?? 0} · ${t('channelMonitorV2.observation.attempts')} ${m.attempt_count ?? 0}`
}
function showTooltip({ target, start }: ActiveSlot) {
  const rect = target.getBoundingClientRect()
  // Anchor to the untransformed track so the lifted segment cannot move the popup.
  const track = target.parentElement?.getBoundingClientRect() || rect
  const above = track.top > 110 || track.bottom + 110 > window.innerHeight
  const width = Math.min(320, Math.max(24, window.innerWidth - 24))
  const anchor = rect.left + rect.width / 2
  const center = Math.min(window.innerWidth - width / 2 - 12, Math.max(width / 2 + 12, anchor))
  lastSlotStart.value = start
  tooltip.value = { slotStart: start, left: center, top: above ? track.top - 10 : track.bottom + 10, above, arrow: Math.min(width - 12, Math.max(12, anchor - center + width / 2)) }
}
function activateTooltip(event: MouseEvent | FocusEvent, slot: { start: string }, kind: 'hover' | 'focus') {
  const target = event.currentTarget as HTMLElement | null
  if (!target) return
  const active = { target, start: slot.start }
  if (kind === 'hover') hoveredSlot = active
  else focusedSlot = active
  showTooltip(active)
}
function leaveTimeline() {
  hoveredSlot = null
  if (focusedSlot) showTooltip(focusedSlot)
  else tooltip.value = null
}
function blurTooltip() {
  focusedSlot = null
  if (hoveredSlot) showTooltip(hoveredSlot)
  else tooltip.value = null
}
function clearTooltip() {
  hoveredSlot = null
  focusedSlot = null
  tooltip.value = null
}
onMounted(() => {
  window.addEventListener('scroll', clearTooltip, true)
  window.addEventListener('resize', clearTooltip)
})
onBeforeUnmount(() => {
  window.removeEventListener('scroll', clearTooltip, true)
  window.removeEventListener('resize', clearTooltip)
})
</script>

<style scoped>
.timeline-segment {
  cursor: crosshair;
  transform-origin: center bottom;
  transition: transform 160ms cubic-bezier(.2, .8, .2, 1), filter 160ms ease, box-shadow 160ms ease;
}
.timeline-segment:focus-visible { transform: translateY(-2px) scaleY(1.18); filter: brightness(1.08); }
@media (hover: hover) and (pointer: fine) {
  .timeline-segment:hover { position: relative; z-index: 1; transform: translateY(-2px) scaleY(1.18); filter: brightness(1.08); box-shadow: 0 4px 10px -4px rgb(15 23 42 / .45); }
}
.timeline-segment:active { transform: scaleY(1.04); }
.timeline-tooltip-bubble::after {
  content: '';
  position: absolute;
  left: var(--tooltip-arrow);
  width: 8px;
  height: 8px;
  background: inherit;
  transform: translateX(-50%) rotate(45deg);
}
.is-above .timeline-tooltip-bubble::after { bottom: -4px; }
.is-below .timeline-tooltip-bubble::after { top: -4px; }
.timeline-tooltip-enter-active, .timeline-tooltip-leave-active { transition: opacity 160ms ease; }
.timeline-tooltip-enter-active .timeline-tooltip-bubble, .timeline-tooltip-leave-active .timeline-tooltip-bubble { transition: transform 160ms cubic-bezier(.2, .8, .2, 1); }
.timeline-tooltip-enter-from, .timeline-tooltip-leave-to { opacity: 0; }
.timeline-tooltip-enter-from .timeline-tooltip-bubble, .timeline-tooltip-leave-to .timeline-tooltip-bubble { transform: translateY(4px) scale(.97); }
.is-below.timeline-tooltip-enter-from .timeline-tooltip-bubble, .is-below.timeline-tooltip-leave-to .timeline-tooltip-bubble { transform: translateY(-4px) scale(.97); }
@media (prefers-reduced-motion: reduce) {
  .timeline-segment, .timeline-tooltip, .timeline-tooltip-bubble { transition: none !important; }
  .timeline-tooltip .timeline-tooltip-bubble { transform: none !important; }
  .timeline-segment:hover, .timeline-segment:focus-visible, .timeline-segment:active { transform: none; }
}
</style>
