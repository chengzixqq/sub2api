<template>
  <div class="flex min-h-6 flex-wrap items-center gap-x-2 gap-y-1 text-xs">
    <span class="badge" :class="tone" :title="reason">{{ t(`channelMonitorV2.unified.states.${state}`) }}</span>
    <span class="text-gray-500 dark:text-gray-400">{{ t(`channelMonitorV2.unified.sources.${status?.source || 'none'}`) }}</span>
    <time v-if="validTime" :datetime="status?.updated_at" class="text-gray-400">{{ validTime }}</time>
  </div>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ObservationCurrentStatus } from '@/api/channelMonitorV2'
const props = defineProps<{ status?: ObservationCurrentStatus; stale?: boolean }>()
const { t, te, locale } = useI18n()
const state = computed(() => props.stale ? 'stale' : props.status?.state || 'unknown')
const tone = computed(() => state.value === 'healthy' ? 'badge-success' : state.value === 'critical' ? 'badge-danger' : state.value === 'warning' || state.value === 'stale' ? 'badge-warning' : 'badge-gray')
const reason = computed(() => {
  if (!props.status?.reason) return ''
  const key = `channelMonitorV2.unified.reasons.${props.status.reason}`
  return te(key) ? t(key) : props.status.reason
})
const validTime = computed(() => props.status?.updated_at && Date.parse(props.status.updated_at) > 0 ? new Date(props.status.updated_at).toLocaleTimeString(locale.value, { hour: '2-digit', minute: '2-digit' }) : '')
</script>
