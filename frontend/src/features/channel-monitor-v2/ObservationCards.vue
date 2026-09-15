<template>
  <section class="space-y-8">
    <div v-if="!overview" class="card flex min-h-48 flex-col items-center justify-center gap-3 p-8 text-sm text-gray-500">
      <span>{{ error ? t('channelMonitorV2.observation.loadFailed') : t('channelMonitorV2.observation.loading') }}</span>
      <button v-if="error" type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="$emit('retry')">
        {{ t('common.retry') }}
      </button>
    </div>
    <template v-else>
      <div class="flex items-center justify-between text-sm text-gray-500 dark:text-gray-400">
        <span>{{ t(`channelMonitorV2.observation.coverage.${overview.coverage.state}`) }}</span>
        <button type="button" class="btn btn-secondary" @click="$emit('toggleLayout')"><Icon name="grid" size="sm" />{{ t(`channelMonitorV2.observation.layout.${layout === 'cards' ? 'matrix' : 'cards'}`) }}</button>
      </div>
      <section v-for="section in sections" :key="section.platform" class="space-y-3">
        <header class="flex items-center gap-2 border-b border-gray-200 pb-2 dark:border-dark-700"><span class="inline-flex h-8 w-8 items-center justify-center rounded-lg" :class="platformBadgeClass(section.platform)"><PlatformIcon :platform="platform(section.platform)" size="md" /></span><h2 class="text-lg font-bold text-gray-900 dark:text-gray-100">{{ label(section.platform) }}</h2><span class="badge badge-gray">{{ section.items.length }}</span></header>
        <div v-if="layout === 'cards'" class="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4"><ObservationGroupCard v-for="item in section.items" :key="item.group_id" :item="item" :coverage="overview.coverage" :admin="admin" @detail="$emit('detail', $event)" /></div>
        <div v-else class="overflow-x-auto rounded-lg border border-gray-200 dark:border-dark-700"><table class="min-w-full text-left text-sm"><thead class="bg-gray-50 text-xs uppercase text-gray-500 dark:bg-dark-800"><tr><th class="px-4 py-3">{{ t('channelMonitorV2.observation.group') }}</th><th class="px-4 py-3">{{ t('channelMonitorV2.observation.reliability') }}</th><th class="px-4 py-3">{{ t('channelMonitorV2.observation.firstOutput') }}</th><th class="px-4 py-3">{{ t('channelMonitorV2.observation.cache') }}</th><th v-if="admin" class="px-4 py-3">{{ t('channelMonitorV2.observation.requests') }}</th><th v-if="admin" class="px-4 py-3">{{ t('channelMonitorV2.observation.errors') }}</th><th v-if="admin" class="px-4 py-3">{{ t('channelMonitorV2.observation.attempts') }}</th></tr></thead><tbody><tr v-for="item in section.items" :key="item.group_id" class="border-t border-gray-100 dark:border-dark-700"><td class="px-4 py-3 font-medium">{{ item.group_name }}</td><td class="px-4 py-3">{{ percent(item.metrics.reliability_rate) }}</td><td class="px-4 py-3">{{ formatMonitorMs(item.metrics.ttft.p50_ms) }}</td><td class="px-4 py-3">{{ percent(item.metrics.cache_rate) }}</td><td v-if="admin" class="px-4 py-3 tabular-nums">{{ item.metrics.request_count ?? 0 }}</td><td v-if="admin" class="px-4 py-3 tabular-nums text-red-600">{{ item.metrics.channel_errors ?? 0 }}</td><td v-if="admin" class="px-4 py-3 tabular-nums">{{ item.metrics.attempt_count ?? 0 }}</td></tr></tbody></table></div>
      </section>
      <div v-if="sections.length === 0" class="card py-12 text-center text-sm text-gray-500 dark:text-gray-400">
        {{ t('channelMonitorV2.observation.empty') }}
      </div>
    </template>
  </section>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import { GROUP_PLATFORM_OPTIONS } from '@/constants/platforms'
import { platformBadgeClass } from '@/utils/platformColors'
import type { ObservationChannel, ObservationOverview } from '@/api/channelMonitorV2'
import type { ObservationLayout } from './observationViewModel'
import { observationSections } from './observationViewModel'
import { formatMonitorMs, formatMonitorPercent } from './monitorFormat'
import ObservationGroupCard from './ObservationGroupCard.vue'
const props = withDefaults(defineProps<{ overview: ObservationOverview | null; layout: ObservationLayout; admin?: boolean; loading?: boolean; error?: boolean }>(), { admin: false, loading: false, error: false })
defineEmits<{ toggleLayout: []; detail: [item: ObservationChannel]; retry: [] }>()
const { t } = useI18n()
const sections = computed(() => observationSections(props.overview?.items || []))
const label = (platform: string) => GROUP_PLATFORM_OPTIONS.find(p => p.value === platform)?.label || platform
const platform = (platform: string) => GROUP_PLATFORM_OPTIONS.find(p => p.value === platform)?.value
const percent = (value: number | null | undefined) => value == null ? '-' : formatMonitorPercent(value)
</script>
