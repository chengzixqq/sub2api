<template>
  <AppLayout>
    <div class="min-w-0 space-y-5 pb-8">
      <header class="page-header mb-0 flex flex-wrap items-start justify-between gap-4 border-b border-gray-200 pb-4 dark:border-dark-700">
        <div class="min-w-0">
          <h1 class="page-title flex items-center gap-2 text-xl font-semibold"><Icon name="chart" size="md" />{{ t('channelMonitorV2.title') }}</h1>
          <div class="mt-2 flex flex-wrap items-center gap-2 text-xs text-gray-500 dark:text-gray-400" role="status">
            <span class="h-2 w-2 shrink-0 rounded-full" :class="collectorTone" />
            <span>{{ t(`channelMonitorV2.unified.collector.${collectorState}`) }}</span>
            <span v-if="isAdmin && overview?.coverage.pending_events != null">{{ t('channelMonitorV2.unified.pendingEvents', { count: overview.coverage.pending_events }) }}</span>
            <span v-if="updatedAt">{{ t('channelMonitorV2.updatedTo', { time: formatTime(updatedAt) }) }}</span>
            <span v-if="loading" class="inline-flex items-center gap-1"><LoadingSpinner size="sm" />{{ t('channelMonitorV2.updating') }}</span>
            <span v-if="overview?.source === 'legacy'" class="badge badge-gray">{{ t('channelMonitorV2.unified.legacySource') }}</span>
          </div>
        </div>
        <button type="button" class="btn btn-secondary btn-icon h-9 w-9 shrink-0" :disabled="loading" :title="t('common.refresh')" :aria-label="t('common.refresh')" @click="refresh"><Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" /></button>
      </header>
      <div v-if="error" role="alert" class="flex flex-wrap items-center justify-between gap-3 rounded-lg border border-amber-200 bg-amber-50 p-3 text-sm text-amber-900 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-200">
        <span>{{ t(`channelMonitorV2.unified.${errorStatus === 403 ? 'accessDenied' : overview ? 'refreshFailed' : 'loadFailed'}`) }}</span>
        <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="refresh">{{ t('common.retry') }}</button>
      </div>
      <div v-else-if="overview && overview.coverage.state !== 'complete'" role="status" class="flex flex-wrap items-center gap-2 text-sm text-amber-700 dark:text-amber-300">
        <span class="badge badge-warning">{{ t(`channelMonitorV2.observation.coverage.${overview.coverage.state}`) }}</span>
        <span v-for="reason in visibleGaps" :key="reason">{{ reason }}</span>
      </div>
      <div class="monitor-toolbar flex flex-wrap items-center gap-2">
        <label v-if="isAdmin" class="inline-flex items-center gap-2 text-xs text-gray-600 dark:text-gray-300"><input v-model="preview" type="checkbox" class="checkbox" />{{ t('channelMonitorV2.unified.preview') }}</label>
        <div class="tabs inline-flex w-auto shrink-0" role="group" :aria-label="t('channelMonitorV2.timeRange')">
          <button v-for="range in ranges" :key="range" type="button" class="tab !px-3 !py-2 text-xs" :class="filter.range === range ? 'tab-active' : ''" :aria-pressed="filter.range === range" @click="filter.range = range">{{ t(`channelMonitorV2.ranges.${range}`) }}</button>
        </div>
        <FilterMultiSelect v-model="filter.platforms" :options="platformOptions" :label="t('channelMonitorV2.filters.platform')" :all-label="t('channelMonitorV2.filters.allPlatforms')" />
        <FilterMultiSelect v-model="selectedGroups" :options="groupOptions" :label="t('channelMonitorV2.filters.group')" :all-label="t('channelMonitorV2.filters.allGroups')" />
        <FilterMultiSelect v-model="filter.models" :options="modelOptions" :label="t('channelMonitorV2.filters.model')" :all-label="t('channelMonitorV2.filters.allModels')" />
        <button v-if="hasFilters" type="button" class="btn btn-secondary btn-icon h-8 w-8" :title="t('channelMonitorV2.clearFilters')" :aria-label="t('channelMonitorV2.clearFilters')" @click="clearFilters"><Icon name="x" size="sm" /></button>
      </div>
      <ObservationCards :overview="overview" :layout="layout" :admin="isAdmin" :loading="loading" :error="error" :stale="stale" @retry="refresh" @toggle-layout="layout = layout === 'cards' ? 'list' : 'cards'" @detail="openDetail" @model="openModelDetail" @bucket="openBucket" />
      <ObservationDetailDrawer :show="Boolean(detail)" :item="detail?.item || null" :model="detail?.model" :selection="detail?.slot || null" :coverage="overview?.coverage" :admin="isAdmin" :stale="stale" :filter="filter" :identity="identity" @close="detail = null" />
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import FilterMultiSelect from '@/features/channel-monitor-v2/FilterMultiSelect.vue'
import ObservationCards from '@/features/channel-monitor-v2/ObservationCards.vue'
import ObservationDetailDrawer from '@/features/channel-monitor-v2/ObservationDetailDrawer.vue'
import { useObservationOverview } from '@/features/channel-monitor-v2/useObservationOverview'
import { observationPreferenceKey, type ObservationLayout } from '@/features/channel-monitor-v2/observationViewModel'
import type { MonitorFilter, MonitorRange, ObservationBucket, ObservationChannel } from '@/api/channelMonitorV2'
import { useAuthStore } from '@/stores/auth'

const { t, te, locale } = useI18n()
const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const isAdmin = computed(() => auth.user?.role === 'admin')
const identity = computed(() => observationPreferenceKey(auth.user?.id, auth.user?.role || 'user', auth.workspace?.id))
const ranges: MonitorRange[] = ['90m', '24h', '7d', '30d']
const csv = (value: unknown) => typeof value === 'string' ? value.split(',').filter(Boolean) : []
const filter = ref<MonitorFilter>({
  range: ranges.includes(route.query.range as MonitorRange) ? route.query.range as MonitorRange : '24h',
  platforms: csv(route.query.platform),
  groupIds: csv(route.query.group).map(Number).filter(value => Number.isInteger(value) && value > 0),
  models: csv(route.query.model),
})
const preview = ref(false)
const { data: overview, loading, error, stale, errorStatus, load } = useObservationOverview(filter, isAdmin, ref(false), identity, preview)
const layout = ref<ObservationLayout>('list')
const detail = ref<{ item: ObservationChannel; model?: string; slot: { start: string; bucket: ObservationBucket | null } | null } | null>(null)
const selectedGroups = computed({ get: () => filter.value.groupIds.map(String), set: (values: string[]) => { filter.value.groupIds = values.map(Number).filter(value => Number.isInteger(value) && value > 0) } })
const dimensions = computed(() => overview.value?.dimensions || { platforms: [], groups: [], models: [] })
const platformOptions = computed(() => dimensions.value.platforms.map(item => ({ value: item.value, label: item.label })))
const platformMatches = (platform?: string) => !platform || !filter.value.platforms.length || filter.value.platforms.includes(platform)
const groupOptions = computed(() => dimensions.value.groups.filter(item => platformMatches(item.platform)).map(item => ({ value: String(item.id), label: item.name || `#${item.id}` })))
const modelOptions = computed(() => dimensions.value.models.filter(item => platformMatches(item.platform)).map(item => ({ value: item.value, label: item.label })))
const hasFilters = computed(() => Boolean(filter.value.platforms.length + filter.value.groupIds.length + filter.value.models.length))
const collectorState = computed(() => stale.value ? 'stale' : overview.value?.coverage.collector_state || (overview.value?.coverage.state === 'complete' ? 'healthy' : 'unknown'))
const collectorTone = computed(() => collectorState.value === 'healthy' ? 'bg-emerald-500' : collectorState.value === 'write_failed' ? 'bg-red-500' : collectorState.value === 'stale' || collectorState.value === 'backlogged' ? 'bg-amber-500' : 'bg-gray-400')
const updatedAt = computed(() => {
  const value = overview.value?.coverage.last_ingested_at || overview.value?.coverage.data_through
  return value && Date.parse(value) > 0 ? value : null
})
const visibleGaps = computed(() => isAdmin.value ? (overview.value?.coverage.gap_reasons || []).map(reason => {
  const key = `channelMonitorV2.unified.gaps.${reason}`
  return te(key) ? t(key) : reason
}) : [])
const formatTime = (value: string) => new Date(value).toLocaleString(locale.value)
function clearFilters() { filter.value = { ...filter.value, platforms: [], groupIds: [], models: [] } }
function refresh() { return load(true, true) }
function openDetail(item: ObservationChannel) { detail.value = { item, slot: null } }
function openModelDetail(item: ObservationChannel, model: string) { detail.value = { item, model, slot: null } }
function openBucket(item: ObservationChannel, slot: { start: string; bucket: ObservationBucket | null }) { detail.value = { item, slot } }

watch(overview, value => {
  if (!detail.value) return
  const item = value?.items.find(candidate => candidate.group_id === detail.value?.item.group_id && candidate.platform === detail.value?.item.platform)
  if (!item || (detail.value.model && !item.models.some(model => model.model === detail.value?.model))) { detail.value = null; return }
  const slot = detail.value.slot
  detail.value = { item, model: detail.value.model, slot: slot ? { start: slot.start, bucket: item.buckets.find(bucket => bucket.bucket_start === slot.start) || null } : null }
}, { flush: 'sync' })
watch(identity, () => {
  detail.value = null
  preview.value = false
  try { layout.value = localStorage.getItem(`${identity.value}:layout`) === 'cards' ? 'cards' : 'list' } catch { layout.value = 'list' }
}, { immediate: true, flush: 'sync' })
watch(layout, value => {
  if (!auth.user?.id) return
  try { localStorage.setItem(`${identity.value}:layout`, value) } catch { /* Preferences are optional. */ }
})
watch([filter, identity, preview], () => {
  detail.value = null
  void router.replace({ query: { range: filter.value.range, platform: filter.value.platforms.join(',') || undefined, group: filter.value.groupIds.join(',') || undefined, model: filter.value.models.join(',') || undefined } })
  void load()
}, { deep: true })

let timer: ReturnType<typeof setInterval> | undefined
function resume() { if (document.visibilityState === 'visible' && !loading.value) void refresh() }
onMounted(() => {
  void load()
  timer = setInterval(resume, 60_000)
  document.addEventListener('visibilitychange', resume)
})
onBeforeUnmount(() => {
  clearInterval(timer)
  document.removeEventListener('visibilitychange', resume)
})
</script>
