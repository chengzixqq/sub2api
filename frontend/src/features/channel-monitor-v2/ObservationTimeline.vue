<template>
  <div class="min-w-0">
    <div class="relative flex min-h-8 gap-[3px] overflow-x-auto py-1" :aria-label="t('channelMonitorV2.observation.history')">
      <span v-for="slot in slots" :key="slot.start" class="relative min-w-[5px] flex-1">
        <button
          type="button"
          class="h-6 w-full rounded-sm focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary-500"
          :class="bucketClass(slot.bucket)"
          :aria-label="label(slot.start, slot.bucket)" :title="label(slot.start, slot.bucket)"
          @mouseenter="hovered = slot.start" @mouseleave="hovered = null"
          @focus="hovered = slot.start" @blur="hovered = null"
          @click="$emit('select', slot)"
        />
        <div v-if="hovered === slot.start" role="tooltip" class="pointer-events-none absolute bottom-full left-1/2 z-30 mb-2 w-max max-w-64 -translate-x-1/2 rounded-lg bg-dark-900 px-2.5 py-1.5 text-[11px] leading-4 text-white shadow-lg">
          {{ label(slot.start, slot.bucket) }}
        </div>
      </span>
    </div>
    <div class="mt-1 flex justify-between gap-2 text-[10px] tabular-nums text-gray-400">
      <span>{{ time(coverage.requested_start) }}</span><span>{{ time(coverage.requested_end || coverage.data_through) }}</span>
    </div>
  </div>
</template>
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ObservationBucket, ObservationOverview } from '@/api/channelMonitorV2'
import { observationTimeline } from './observationViewModel'
import { formatMonitorPercent } from './monitorFormat'
const props = defineProps<{ buckets: ObservationBucket[]; coverage: ObservationOverview['coverage'] }>()
defineEmits<{ select: [slot: { start: string; bucket: ObservationBucket | null }] }>()
const { t, locale } = useI18n()
const slots = computed(() => observationTimeline(props.buckets, props.coverage))
const hovered = ref<string | null>(null)
const percent = (value: number | null | undefined) => value == null ? '-' : formatMonitorPercent(value)
function time(value: string) {
  const date = new Date(value)
  return Number.isFinite(date.getTime()) ? date.toLocaleString(locale.value, { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }) : '-'
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
  return `${time(start)} · ${t(`channelMonitorV2.observation.states.${status}`)}${bucket ? ` · ${percent(bucket.metrics.reliability_rate)}` : ''}`
}
</script>
