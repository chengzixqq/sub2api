<template>
  <article class="card flex h-full min-w-0 flex-col !rounded-lg p-5" :data-group-id="item.group_id">
    <div class="flex items-start gap-3">
      <span class="inline-flex h-10 w-10 shrink-0 items-center justify-center rounded-lg" :class="platformBadgeClass(item.platform)"><PlatformIcon :platform="platform" size="md" /></span>
      <div class="min-w-0 flex-1">
        <button type="button" class="break-words text-left text-sm font-semibold leading-6 text-gray-900 hover:text-primary-600 focus-visible:outline-primary-500 dark:text-gray-100" :aria-expanded="expanded" @click="toggleExpanded">{{ item.group_name }}</button>
        <div class="mt-1 flex flex-wrap gap-x-2 gap-y-1 text-xs text-gray-500 dark:text-gray-400">
          <span v-if="item.rate_multiplier != null" class="text-primary-600 dark:text-primary-400">{{ t('channelMonitorV2.observation.multiplier', { value: item.rate_multiplier }) }}</span>
          <span v-else>{{ t('channelMonitorV2.observation.priceUnavailable') }}</span>
        </div>
      </div>
    </div>
    <ObservationStatus class="mt-3 min-h-6" :metrics="item.metrics" :health="item.health" :coverage="coverage" />
    <ObservationMetrics class="my-5" :metrics="item.metrics" :health="item.health" :unavailable="coverage.state === 'unavailable'" />
    <div v-if="expanded" class="mb-4 border-t border-gray-100 pt-3 dark:border-dark-700">
      <p class="mb-2 text-xs font-semibold uppercase tracking-wide text-gray-400">{{ t('channelMonitorV2.observation.modelDetails') }}</p>
      <div v-if="item.models.length" class="space-y-2">
        <div v-for="model in item.models" :key="model.model" class="flex items-center justify-between gap-3 rounded-lg bg-gray-50 px-3 py-2 text-xs dark:bg-dark-900/40">
          <span class="min-w-0 truncate font-medium text-gray-700 dark:text-gray-200">{{ model.model }}</span>
          <span class="shrink-0 tabular-nums text-gray-500 dark:text-gray-400">{{ percent(model.metrics.reliability_rate) }} · {{ formatMonitorMs(model.metrics.ttft.p50_ms) }}</span>
        </div>
      </div>
      <p v-else class="text-xs text-gray-400">{{ t('channelMonitorV2.observation.noModels') }}</p>
    </div>
    <div class="mt-auto">
      <div class="mb-2 flex items-center justify-between gap-2 text-xs text-gray-400">
        <span>{{ t('channelMonitorV2.observation.history') }}</span>
        <button type="button" class="inline-flex items-center gap-1 text-primary-600 dark:text-primary-400" :aria-expanded="expanded" @click="toggleExpanded">{{ t('channelMonitorV2.observation.models', { count: item.models.length }) }}<Icon name="chevronRight" size="xs" :class="expanded ? 'rotate-90' : ''" /></button>
      </div>
      <ObservationTimeline :buckets="item.buckets" :coverage="coverage" @select="$emit('bucket', item, $event)" />
    </div>
  </article>
</template>
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import { platformBadgeClass } from '@/utils/platformColors'
import { GROUP_PLATFORM_OPTIONS } from '@/constants/platforms'
import { formatMonitorMs, formatMonitorPercent } from './monitorFormat'
import type { ObservationBucket, ObservationChannel, ObservationOverview } from '@/api/channelMonitorV2'
import ObservationMetrics from './ObservationMetrics.vue'
import ObservationTimeline from './ObservationTimeline.vue'
import ObservationStatus from './ObservationStatus.vue'
const props = defineProps<{ item: ObservationChannel; coverage: ObservationOverview['coverage'] }>()
defineEmits<{ detail: [item: ObservationChannel]; bucket: [item: ObservationChannel, slot: { start: string; bucket: ObservationBucket | null }] }>()
const { t } = useI18n()
const platform = computed(() => GROUP_PLATFORM_OPTIONS.find(p => p.value === props.item.platform)?.value)
const expanded = ref(false)
const percent = (value: number | null | undefined) => value == null ? '-' : formatMonitorPercent(value)
function toggleExpanded() {
  expanded.value = !expanded.value
}
</script>
