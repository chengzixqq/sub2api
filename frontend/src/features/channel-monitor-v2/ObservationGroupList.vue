<template>
  <div class="divide-y divide-gray-200 border-y border-gray-200 dark:divide-dark-700 dark:border-dark-700">
    <article v-for="item in items" :key="item.group_id" class="min-w-0 py-4" :data-group-id="item.group_id">
      <div class="grid grid-cols-[minmax(0,1fr)_32px] items-start gap-x-5 gap-y-4 lg:grid-cols-[minmax(0,1fr)_minmax(260px,1.2fr)_140px_32px]">
        <div class="min-w-0">
          <div class="flex items-start gap-2">
            <PlatformIcon :platform="platform(item.platform)" size="sm" class="mt-1 shrink-0" />
            <div class="min-w-0">
              <button type="button" class="break-words text-left text-sm font-semibold text-gray-900 hover:text-primary-600 dark:text-gray-100" @click="emit('detail', item)">{{ item.group_name }}</button>
              <p class="mt-1 text-xs text-gray-500">{{ platformLabel(item.platform) }}<span v-if="item.rate_multiplier != null" class="ml-2 text-primary-600 dark:text-primary-400">{{ t('channelMonitorV2.observation.multiplier', { value: item.rate_multiplier }) }}</span></p>
            </div>
          </div>
          <ObservationCurrentStatus class="mt-2" :status="item.current_status" :stale="stale" />
        </div>
        <ObservationMetrics class="col-span-2 row-start-2 lg:col-span-1 lg:row-auto" :metrics="item.traffic || item.metrics" :health="item.health" :unavailable="coverage.state === 'unavailable'" />
        <dl class="col-span-2 flex flex-wrap gap-x-6 gap-y-2 text-xs lg:col-span-1 lg:block lg:space-y-2">
          <div class="flex items-center gap-2"><dt class="text-gray-500">{{ t('channelMonitorV2.unified.probe') }}</dt><dd>{{ statusLabel(item.probe?.status) }}</dd></div>
          <div class="flex items-center gap-2"><dt class="text-gray-500">{{ t('channelMonitorV2.unified.quota') }}</dt><dd>{{ statusLabel(item.quota?.status) }}</dd></div>
          <div v-if="admin" class="flex items-center gap-2"><dt class="text-gray-500">{{ t('channelMonitorV2.observation.requests') }}</dt><dd class="tabular-nums">{{ item.metrics.request_count ?? 0 }}</dd></div>
        </dl>
        <button type="button" class="btn btn-secondary btn-icon col-start-2 row-start-1 h-8 w-8 lg:col-start-4" :data-testid="`expand-group-${item.group_id}`" :aria-expanded="expanded.has(item.group_id)" :title="t('channelMonitorV2.observation.models', { count: item.models.length })" :aria-label="t('channelMonitorV2.observation.models', { count: item.models.length })" @click="toggle(item.group_id)"><Icon name="chevronRight" size="sm" :class="expanded.has(item.group_id) ? 'rotate-90' : ''" /></button>
      </div>
      <ObservationTimeline class="mt-3" :buckets="item.buckets" :coverage="coverage" :admin="admin" @select="emit('bucket', item, $event)" />
      <div v-if="expanded.has(item.group_id)" class="mt-4 border-l-2 border-gray-200 pl-4 dark:border-dark-700">
        <p v-if="!item.models.length" class="py-2 text-sm text-gray-500">{{ t('channelMonitorV2.observation.noModels') }}</p>
        <div v-for="model in item.models" :key="model.model" class="grid min-w-0 grid-cols-1 items-start gap-3 border-t border-gray-100 py-3 first:border-0 lg:grid-cols-[minmax(0,1fr)_minmax(260px,1fr)] dark:border-dark-700">
          <div class="min-w-0"><button type="button" :data-testid="`model-${model.model}`" class="break-all text-left text-sm font-medium text-primary-600 dark:text-primary-400" @click="emit('model', item, model.model)">{{ model.model }}</button><ObservationCurrentStatus class="mt-2" :status="model.current_status" :stale="stale" /></div>
          <ObservationMetrics :metrics="model.traffic || model.metrics" :health="model.health" :unavailable="coverage.state === 'unavailable'" />
        </div>
      </div>
    </article>
  </div>
</template>
<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import { GROUP_PLATFORM_OPTIONS } from '@/constants/platforms'
import type { ObservationBucket, ObservationChannel, ObservationOverview } from '@/api/channelMonitorV2'
import ObservationCurrentStatus from './ObservationCurrentStatus.vue'
import ObservationMetrics from './ObservationMetrics.vue'
import ObservationTimeline from './ObservationTimeline.vue'
withDefaults(defineProps<{ items: ObservationChannel[]; coverage: ObservationOverview['coverage']; admin?: boolean; stale?: boolean }>(), { admin: false, stale: false })
const emit = defineEmits<{ detail: [item: ObservationChannel]; model: [item: ObservationChannel, model: string]; bucket: [item: ObservationChannel, slot: { start: string; bucket: ObservationBucket | null }] }>()
const { t, te } = useI18n()
const expanded = ref(new Set<number>())
const platform = (value: string) => GROUP_PLATFORM_OPTIONS.find(item => item.value === value)?.value
const platformLabel = (value: string) => { const key = `channelMonitorV2.platforms.${value}`; return te(key) ? t(key) : GROUP_PLATFORM_OPTIONS.find(item => item.value === value)?.label || value }
function toggle(id: number) {
  const next = new Set(expanded.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expanded.value = next
}
function statusLabel(value?: string) {
  const key = `channelMonitorV2.unified.states.${value || 'unknown'}`
  return te(key) ? t(key) : value || t('channelMonitorV2.unified.states.unknown')
}
</script>
