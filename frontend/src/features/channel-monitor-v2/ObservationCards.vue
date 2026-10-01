<template>
  <section class="space-y-6">
    <div v-if="!overview" class="flex min-h-48 flex-col items-center justify-center gap-3 py-8 text-sm text-gray-500">
      <span>{{ error ? t('channelMonitorV2.observation.loadFailed') : t('channelMonitorV2.observation.loading') }}</span>
      <button v-if="error" type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="$emit('retry')">{{ t('common.retry') }}</button>
    </div>
    <template v-else>
      <div class="flex items-center justify-between gap-3 text-xs text-gray-500 dark:text-gray-400">
        <span>{{ t('channelMonitorV2.unified.historyInterval', { minutes: Math.round(overview.coverage.bucket_seconds / 60) }) }}</span>
        <button v-if="!view" type="button" class="btn btn-secondary btn-icon h-8 w-8" :title="t(`channelMonitorV2.observation.layout.${activeLayout === 'cards' ? 'list' : 'cards'}`)" :aria-label="t(`channelMonitorV2.observation.layout.${activeLayout === 'cards' ? 'list' : 'cards'}`)" @click="$emit('toggleLayout')"><Icon :name="activeLayout === 'cards' ? 'menu' : 'grid'" size="sm" /></button>
      </div>
      <div v-if="activeLayout === 'cards'" class="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
        <ObservationGroupCard v-for="item in orderedItems" :key="item.group_id" :item="item" :coverage="overview.coverage" :source="overview.source" :admin="admin" :stale="stale" @detail="emit('detail', $event)" @bucket="handleBucket" />
      </div>
      <ObservationGroupList v-else :items="orderedItems" :coverage="overview.coverage" :admin="admin" :stale="stale" @detail="emit('detail', $event)" @model="(item, model) => emit('model', item, model)" @bucket="handleBucket" />
      <div v-if="sections.length === 0" class="py-12 text-center text-sm text-gray-500 dark:text-gray-400">{{ t('channelMonitorV2.observation.empty') }}</div>
    </template>
  </section>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { ObservationBucket, ObservationChannel, ObservationOverview } from '@/api/channelMonitorV2'
import type { ObservationLayout } from './observationViewModel'
import { observationSections } from './observationViewModel'
import ObservationGroupCard from './ObservationGroupCard.vue'
import ObservationGroupList from './ObservationGroupList.vue'
const props = withDefaults(defineProps<{ overview: ObservationOverview | null; layout: ObservationLayout; view?: 'cards' | 'list'; admin?: boolean; loading?: boolean; error?: boolean; stale?: boolean }>(), { admin: false, loading: false, error: false, stale: false })
const emit = defineEmits<{ toggleLayout: []; detail: [item: ObservationChannel]; model: [item: ObservationChannel, model: string]; bucket: [item: ObservationChannel, slot: { start: string; bucket: ObservationBucket | null }]; retry: [] }>()
const { t } = useI18n()
const activeLayout = computed(() => props.view === 'cards' || props.view === 'list' ? props.view : props.layout)
const sections = computed(() => observationSections(props.overview?.items || []))
const orderedItems = computed(() => sections.value.flatMap(section => section.items))
function handleBucket(item: ObservationChannel, slot: { start: string; bucket: ObservationBucket | null }) { emit('bucket', item, slot) }
</script>
