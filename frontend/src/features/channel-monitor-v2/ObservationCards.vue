<template>
  <section class="observation-cards space-y-6">
    <div v-if="!overview" class="flex min-h-48 flex-col items-center justify-center gap-3 py-8 text-sm text-gray-500">
      <span>{{ error ? t('channelMonitorV2.observation.loadFailed') : t('channelMonitorV2.observation.loading') }}</span>
      <button v-if="error" type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="$emit('retry')">{{ t('common.retry') }}</button>
    </div>
    <template v-else>
      <div class="flex items-center justify-between gap-3 text-xs text-gray-500 dark:text-gray-400">
        <span>{{ t('channelMonitorV2.unified.historyInterval', { minutes: Math.round(overview.coverage.bucket_seconds / 60) }) }}</span>
        <button v-if="!view" type="button" class="btn btn-secondary btn-icon h-8 w-8" :title="t(`channelMonitorV2.observation.layout.${activeLayout === 'cards' ? 'list' : 'cards'}`)" :aria-label="t(`channelMonitorV2.observation.layout.${activeLayout === 'cards' ? 'list' : 'cards'}`)" @click="$emit('toggleLayout')"><Icon :name="activeLayout === 'cards' ? 'menu' : 'grid'" size="sm" /></button>
      </div>
      <div :key="activeLayout" class="observation-layout-reveal">
      <div v-if="activeLayout === 'cards'" class="space-y-7">
        <section v-for="section in sections" :key="section.platform" class="space-y-4" :aria-label="platformLabel(section.platform)">
          <header class="flex items-center gap-3 px-1">
            <span class="inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-xl" :class="platformBadgeClass(section.platform)"><PlatformIcon :platform="platformValue(section.platform)" size="sm" /></span>
            <h2 class="text-lg font-semibold tracking-tight text-slate-800 dark:text-gray-100">{{ platformLabel(section.platform) }}</h2>
            <span class="rounded-lg border border-white/80 bg-white/70 px-2 py-0.5 text-xs tabular-nums text-slate-500 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-400">{{ section.items.length }}</span>
          </header>
          <div class="observation-card-grid">
            <ObservationGroupCard v-for="item in section.items" :key="`${item.platform}:${item.group_id}`" :item="item" :coverage="overview.coverage" :source="overview.source" :admin="admin" :stale="stale" @detail="emit('detail', $event)" @bucket="handleBucket" />
          </div>
        </section>
      </div>
      <ObservationGroupList v-else :items="orderedItems" :coverage="overview.coverage" :admin="admin" :stale="stale" @detail="emit('detail', $event)" @model="(item, model) => emit('model', item, model)" @bucket="handleBucket" />
      </div>
      <div v-if="sections.length === 0" class="py-12 text-center text-sm text-gray-500 dark:text-gray-400">{{ t('channelMonitorV2.observation.empty') }}</div>
    </template>
  </section>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import { platformBadgeClass } from '@/utils/platformColors'
import { GROUP_PLATFORM_OPTIONS } from '@/constants/platforms'
import type { ObservationBucket, ObservationChannel, ObservationOverview } from '@/api/channelMonitorV2'
import type { ObservationLayout } from './observationViewModel'
import { observationSections } from './observationViewModel'
import ObservationGroupCard from './ObservationGroupCard.vue'
import ObservationGroupList from './ObservationGroupList.vue'
const props = withDefaults(defineProps<{ overview: ObservationOverview | null; layout: ObservationLayout; view?: 'cards' | 'list'; admin?: boolean; loading?: boolean; error?: boolean; stale?: boolean }>(), { admin: false, loading: false, error: false, stale: false })
const emit = defineEmits<{ toggleLayout: []; detail: [item: ObservationChannel]; model: [item: ObservationChannel, model: string]; bucket: [item: ObservationChannel, slot: { start: string; bucket: ObservationBucket | null }]; retry: [] }>()
const { t, te } = useI18n()
const platformValue = (value: string) => GROUP_PLATFORM_OPTIONS.find(item => item.value === value)?.value
const platformLabel = (value: string) => {
  const key = `channelMonitorV2.platforms.${value}`
  return te(key) ? t(key) : GROUP_PLATFORM_OPTIONS.find(item => item.value === value)?.label || value
}
const activeLayout = computed(() => props.view === 'cards' || props.view === 'list' ? props.view : props.layout)
const sections = computed(() => observationSections(props.overview?.items || []))
const orderedItems = computed(() => sections.value.flatMap(section => section.items))
function handleBucket(item: ObservationChannel, slot: { start: string; bucket: ObservationBucket | null }) { emit('bucket', item, slot) }
</script>

<style scoped>
.observation-cards { container-type: inline-size; }
.observation-card-grid { display: grid; grid-template-columns: minmax(0, 1fr); gap: 20px; }
@container (min-width: 720px) {
  .observation-card-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
@container (min-width: 1160px) {
  .observation-card-grid { grid-template-columns: repeat(3, minmax(0, 440px)); }
}
.observation-layout-reveal { animation: observation-layout-in 180ms ease-out; }
@keyframes observation-layout-in { from { opacity: 0; } to { opacity: 1; } }
@media (prefers-reduced-motion: reduce) {
  .observation-layout-reveal { animation: none; }
}
</style>
