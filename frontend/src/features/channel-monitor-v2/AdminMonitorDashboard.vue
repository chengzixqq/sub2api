<template>
  <section class="space-y-5" data-testid="admin-monitor-dashboard">
    <MonitorOverviewHero :eyebrow="text('运营监控', 'Operations monitor')" :title="text('渠道控制台', 'Channel control room')" :subtitle="text('真实流量、主动探测与额度同步分别呈现，快速定位故障来源。', 'Traffic, probes and quota remain separate to identify the source of a problem.')" :stats="heroStats" />

    <div class="monitor-toolbar flex flex-wrap items-center gap-2 rounded-xl border border-gray-200 bg-white p-3 dark:border-dark-700 dark:bg-dark-800">
      <label class="sr-only" :for="rangeId">{{ t('channelMonitorV2.timeRange') }}</label>
      <select :id="rangeId" v-model="filter.range" class="input !w-auto !py-1.5 text-xs" :disabled="previewMode" :aria-describedby="previewMode ? rangeId + '-hint' : undefined"><option v-for="range in ranges" :key="range" :value="range">{{ t('channelMonitorV2.ranges.' + range) }}</option></select>
      <span v-if="previewMode" :id="rangeId + '-hint'" class="text-xs text-gray-500">{{ text('预览固定 24 小时数据', 'Preview uses a fixed 24h fixture window') }}</span>
      <FilterMultiSelect v-model="filter.platforms" compact :label="t('channelMonitorV2.filters.platform')" :all-label="t('channelMonitorV2.filters.allPlatforms')" :options="platformOptions" />
      <FilterMultiSelect v-model="selectedGroups" compact :label="t('channelMonitorV2.filters.group')" :all-label="t('channelMonitorV2.filters.allGroups')" :options="groupOptions" />
      <FilterMultiSelect v-model="filter.models" compact :label="t('channelMonitorV2.filters.model')" :all-label="t('channelMonitorV2.filters.allModels')" :options="modelOptions" />
      <button v-if="hasFilters" type="button" class="btn btn-secondary btn-sm" @click="clearFilters">{{ t('channelMonitorV2.clearFilters') }}</button>
      <button type="button" class="btn btn-secondary btn-sm ml-auto" :disabled="loading" @click="refresh">{{ t('common.refresh') }}</button>
    </div>

    <div v-if="error" role="alert" class="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-amber-200 bg-amber-50 p-3 text-sm text-amber-900 dark:border-amber-900/60 dark:bg-amber-950/30 dark:text-amber-200">
      <span>{{ overview ? t('channelMonitorV2.unified.refreshFailed') : t('channelMonitorV2.unified.loadFailed') }}</span>
      <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="refresh">{{ t('common.retry') }}</button>
    </div>
    <MonitorEvidenceStatus v-if="overview" :overview="overview" :stale="stale" />
    <MonitorViewSwitcher v-model="view" :label="text('监控视图', 'Monitor view')" :items="viewItems">
      <span class="text-xs text-gray-500 dark:text-gray-400">{{ loading ? t('channelMonitorV2.updating') : updatedLabel }}</span>
    </MonitorViewSwitcher>

    <template v-if="overview">
      <ObservationCards v-if="view === 'cards'" :overview="overview" layout="cards" view="cards" admin :loading="loading" :stale="stale" @detail="openDetail" @bucket="openBucket" />
      <MonitorHealthMatrix v-else-if="view === 'matrix'" :items="overview.items" :coverage="overview.coverage" :source="overview.source" @detail="openDetail" @model="openModel" @bucket="openBucket" />
      <div v-else class="grid grid-cols-1 items-start gap-5 xl:grid-cols-[1.25fr_0.75fr]">
        <div class="min-w-0 space-y-5">
          <MonitorTrendPanel :items="overview.items" :coverage="overview.coverage" :source="overview.source" />
          <MonitorErrorBreakdown :items="errorItems" :source="errorSource" :loading="errorsLoading" :failed="errorsFailed" @retry="loadErrors" />
        </div>
        <section class="space-y-4 rounded-2xl border border-gray-200 bg-white p-5 shadow-sm dark:border-dark-700 dark:bg-dark-800">
          <header><h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ text('运维证据', 'Operations evidence') }}</h3><p class="mt-1 text-xs text-gray-500">{{ text('每种信号只回答自己的问题。', 'Each signal answers a different question.') }}</p></header>
          <dl class="space-y-3 text-xs">
            <div class="rounded-xl bg-emerald-50 p-3 dark:bg-emerald-950/20"><dt class="text-emerald-700 dark:text-emerald-300">{{ t('channelMonitorV2.unified.traffic') }}</dt><dd class="mt-1 font-semibold text-gray-800 dark:text-gray-100">{{ trafficSummary }}</dd></div>
            <div class="rounded-xl bg-sky-50 p-3 dark:bg-sky-950/20"><dt class="text-sky-700 dark:text-sky-300">{{ t('channelMonitorV2.unified.probe') }}</dt><dd class="mt-1 font-semibold text-gray-800 dark:text-gray-100">{{ probeSummary }}</dd></div>
            <div class="rounded-xl bg-violet-50 p-3 dark:bg-violet-950/20"><dt class="text-violet-700 dark:text-violet-300">{{ t('channelMonitorV2.unified.quota') }}</dt><dd class="mt-1 font-semibold text-gray-800 dark:text-gray-100">{{ quotaSummary }}</dd></div>
          </dl>
          <ul class="space-y-2 border-t border-gray-100 pt-4 dark:border-dark-700"><li v-for="item in overview.items" :key="item.platform + ':' + item.group_id"><button type="button" class="flex w-full items-center justify-between gap-2 rounded-lg px-2 py-2 text-left text-xs hover:bg-gray-50 dark:hover:bg-dark-700" @click="openDetail(item)"><span class="truncate font-medium text-gray-700 dark:text-gray-200">{{ item.group_name }}</span><span class="badge badge-gray">{{ sourceDisplayLabel(item.source || overview.source) }}</span></button></li></ul>
        </section>
      </div>
    </template>
    <div v-else-if="loading" class="grid grid-cols-1 gap-4 md:grid-cols-3" :aria-label="t('common.loading')"><div v-for="n in 3" :key="n" class="h-52 animate-pulse rounded-2xl bg-gray-100 dark:bg-dark-800" /></div>

    <ObservationDetailDrawer :show="Boolean(detail)" :item="detail?.item || null" :model="detail?.model" :selection="detail?.slot || null" :coverage="overview?.coverage" :source="overview?.source" admin :owner="auth.isOwner && !previewMode" :preview="previewMode" :stale="stale" :filter="filter" :identity="identity" @close="detail = null" />
  </section>
</template>

<script setup lang="ts">
import { computed, getCurrentInstance, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { getErrors, type MonitorErrorRow, type MonitorFilter, type MonitorRange, type ObservationBucket, type ObservationChannel, type ObservationOverview } from '@/api/channelMonitorV2'
import { useObservationOverview } from './useObservationOverview'
import FilterMultiSelect from './FilterMultiSelect.vue'
import { monitorSourceLabel } from './monitorLabels'
import ObservationCards from './ObservationCards.vue'
import ObservationDetailDrawer from './ObservationDetailDrawer.vue'
import MonitorErrorBreakdown from './MonitorErrorBreakdown.vue'
import MonitorEvidenceStatus from './MonitorEvidenceStatus.vue'
import MonitorHealthMatrix from './MonitorHealthMatrix.vue'
import MonitorOverviewHero from './MonitorOverviewHero.vue'
import MonitorTrendPanel from './MonitorTrendPanel.vue'
import MonitorViewSwitcher from './MonitorViewSwitcher.vue'

const props = withDefaults(defineProps<{ filter?: MonitorFilter; previewOverview?: ObservationOverview | null; preview?: boolean }>(), { filter: () => ({ range: '24h', platforms: [], groupIds: [], models: [] }), previewOverview: null, preview: false })
const emit = defineEmits<{ detail: [item: ObservationChannel] }>()
const { t, te, locale } = useI18n()
const auth = useAuthStore()
const text = (zh: string, en: string) => locale.value.startsWith('zh') ? zh : en
const rangeId = 'admin-monitor-range-' + getCurrentInstance()?.uid
const ranges: MonitorRange[] = ['90m', '24h', '7d', '30d']
const filter = ref<MonitorFilter>({ ...props.filter, platforms: [...props.filter.platforms], groupIds: [...props.filter.groupIds], models: [...props.filter.models] })
const identity = computed(() => ['admin-monitor', auth.user?.id ?? 'anonymous', auth.user?.role ?? 'unknown', auth.workspace?.id ?? 'owner'].join(':'))
const previewMode = computed(() => props.preview || Boolean(props.previewOverview))
const { data, loading, error, stale, load, cancel } = useObservationOverview(filter, ref(true), ref(false), identity)
const overview = computed(() => {
  const raw = previewMode.value ? props.previewOverview : data.value
  if (!raw || !previewMode.value) return raw
  const items = raw.items.filter(item => (!filter.value.platforms.length || filter.value.platforms.includes(item.platform)) && (!filter.value.groupIds.length || filter.value.groupIds.includes(item.group_id))).map(item => ({ ...item, models: item.models.filter(model => !filter.value.models.length || filter.value.models.includes(model.model)) }))
  return { ...raw, items: items.filter(item => !filter.value.models.length || item.models.length > 0) }
})
const view = ref<'cards' | 'matrix' | 'advanced'>('cards')
const viewItems = computed(() => [{ value: 'cards', label: text('卡片', 'Cards') }, { value: 'matrix', label: text('矩阵', 'Matrix') }, { value: 'advanced', label: text('高级大屏', 'Advanced') }])
const selectedGroups = computed<string[]>({ get: () => filter.value.groupIds.map(String), set: values => { filter.value.groupIds = values.map(Number).filter(value => Number.isInteger(value) && value > 0) } })
const dimensions = computed(() => overview.value?.dimensions || { platforms: [], groups: [], models: [] })
const platformOptions = computed(() => dimensions.value.platforms.map(item => { const key = `channelMonitorV2.platforms.${item.value}`; return { value: item.value, label: te(key) ? t(key) : item.label } }))
const platformMatches = (platform?: string) => !platform || !filter.value.platforms.length || filter.value.platforms.includes(platform)
const groupOptions = computed(() => dimensions.value.groups.filter(item => platformMatches(item.platform)).map(item => ({ value: String(item.id), label: item.name || '#' + item.id })))
const modelOptions = computed(() => dimensions.value.models.filter(item => platformMatches(item.platform)).map(item => ({ value: item.value, label: item.label })))
const hasFilters = computed(() => Boolean(filter.value.platforms.length + filter.value.groupIds.length + filter.value.models.length))
const number = (value: number) => new Intl.NumberFormat(locale.value).format(value)
const percent = (value: number | null) => value == null ? '—' : (value * 100).toFixed(1) + '%'
const items = computed(() => overview.value?.items || [])
const trafficAvailable = computed(() => overview.value?.coverage.state !== 'unavailable')
const sampled = computed(() => trafficAvailable.value ? items.value.filter(item => (item.traffic || item.metrics).sample_state === 'sufficient') : [])
const healthy = computed(() => sampled.value.filter(item => item.health.reliability === 'healthy' && item.health.latency === 'healthy').length)
const warning = computed(() => sampled.value.filter(item => item.health.reliability !== 'critical' && item.health.latency !== 'critical' && (item.health.reliability === 'warning' || item.health.latency === 'warning')).length)
const critical = computed(() => sampled.value.filter(item => item.health.reliability === 'critical' || item.health.latency === 'critical').length)
// Global counters keep compact and legacy separate because their attempt/terminal semantics differ.
const counters = computed(() => ['compact', 'legacy', 'terminal_v1'].map(source => {
  const selected = items.value.filter(item => (item.source || overview.value?.source) === source)
  const requests = selected.reduce((sum, item) => sum + ((item.traffic || item.metrics).request_count || 0), 0)
  const errors = selected.reduce((sum, item) => sum + ((item.traffic || item.metrics).channel_errors || 0), 0)
  const successes = selected.reduce((sum, item) => sum + ((item.traffic || item.metrics).success_requests || 0), 0)
  return { source, selected, requests, errors, successes }
}).filter(item => item.selected.length))
const requestLabel = computed(() => overview.value && trafficAvailable.value ? counters.value.map(item => number(item.requests)).join(' / ') || '0' : '—')
const errorLabel = computed(() => overview.value && trafficAvailable.value ? counters.value.map(item => percent(item.successes + item.errors ? item.errors / (item.successes + item.errors) : null)).join(' / ') || '—' : '—')
const sourceDisplayLabel = (source?: string) => monitorSourceLabel(source, t, te)
const sourceLabel = computed(() => counters.value.map(item => sourceDisplayLabel(item.source)).join(' / ') || sourceDisplayLabel(overview.value?.source) || '—')
const probeItems = computed(() => items.value.filter(item => item.probe && ['healthy', 'degraded', 'warning', 'unavailable', 'critical', 'error'].includes(item.probe.status)))
const probeOK = computed(() => probeItems.value.filter(item => item.probe?.status === 'healthy').length)
const quotaItems = computed(() => items.value.filter(item => item.quota && !['not_configured', 'unsupported', 'unknown'].includes(item.quota.status)))
const quotaOK = computed(() => quotaItems.value.filter(item => ['ok', 'healthy'].includes(item.quota?.status || '')).length)
const heroStats = computed(() => [
  { label: text('活跃渠道 / 全部', 'Active / all channels'), value: overview.value && trafficAvailable.value ? number(items.value.filter(item => (item.traffic || item.metrics).sample_state !== 'no_samples').length) + ' / ' + number(items.value.length) : '—', detail: text('真实请求有样本', 'Real traffic samples') },
  { label: text('正常 / 警告 / 异常', 'Healthy / warning / critical'), value: overview.value && trafficAvailable.value ? healthy.value + ' / ' + warning.value + ' / ' + critical.value : '—', detail: text('仅充分的真实流量样本', 'Sufficient traffic samples only') },
  { label: text('请求量 · ', 'Requests · ') + t(`channelMonitorV2.ranges.${filter.value.range}`), value: requestLabel.value, detail: sourceLabel.value, tone: 'text-sky-600 dark:text-sky-400' },
  { label: text('渠道错误率', 'Channel error rate'), value: errorLabel.value, detail: sourceLabel.value, tone: 'text-red-600 dark:text-red-400' },
  { label: text('最近探针成功比例', 'Latest probe success share'), value: percent(probeItems.value.length ? probeOK.value / probeItems.value.length : null), detail: text('不是历史探针成功率', 'Not historical probe success rate'), tone: 'text-cyan-600 dark:text-cyan-400' },
  { label: text('额度同步正常', 'Quota sync OK'), value: quotaItems.value.length ? quotaOK.value + ' / ' + quotaItems.value.length : '—', detail: text('独立于流量可靠性', 'Independent of traffic reliability'), tone: 'text-violet-600 dark:text-violet-400' },
])
const trafficSummary = computed(() => number(sampled.value.length) + ' / ' + number(items.value.length) + ' ' + text('样本充足', 'sufficient samples'))
const probeSummary = computed(() => probeItems.value.length ? probeOK.value + ' / ' + probeItems.value.length + ' ' + text('最近探测成功', 'latest checks healthy') : text('没有探测结果', 'No probe results'))
const quotaSummary = computed(() => quotaItems.value.length ? quotaOK.value + ' / ' + quotaItems.value.length + ' ' + text('同步正常', 'sync OK') : text('没有额度快照', 'No quota snapshots'))
const updatedLabel = computed(() => { const value = overview.value?.coverage.data_through; return value && Date.parse(value) > 0 ? t('channelMonitorV2.updatedTo', { time: new Date(value).toLocaleString(locale.value) }) : '—' })
const detail = ref<{ item: ObservationChannel; model?: string; slot: { start: string; bucket: ObservationBucket | null } | null } | null>(null)
function openDetail(item: ObservationChannel) { detail.value = { item, slot: null }; emit('detail', item) }
function openModel(item: ObservationChannel, model: string) { detail.value = { item, model, slot: null } }
function openBucket(item: ObservationChannel, slot: { start: string; bucket: ObservationBucket | null }) { detail.value = { item, slot } }
watch(overview, value => { if (!detail.value) return; const item = value?.items.find(item => item.group_id === detail.value?.item.group_id && item.platform === detail.value.item.platform); detail.value = item ? { ...detail.value, item } : null }, { flush: 'sync' })
const remoteErrors = ref<MonitorErrorRow[]>([])
const errorsLoading = ref(false)
const errorsFailed = ref(false)
const errorSource = computed(() => overview.value?.source === 'legacy' || overview.value?.source === 'terminal_v1' ? overview.value.source : overview.value?.source || 'compact')
// Compact error taxonomies already accompany the selected overview. Legacy diagnostics are fetched only when opened.
const errorItems = computed(() => {
  if (!previewMode.value && ['legacy', 'terminal_v1'].includes(overview.value?.source || '')) return remoteErrors.value
  const categories = new Map<string, number>()
  for (const item of items.value) for (const [category, count] of Object.entries((item.traffic || item.metrics).error_categories || {})) { const key = (overview.value?.source === 'mixed' ? (item.source || 'unknown') + ' · ' : '') + category; categories.set(key, (categories.get(key) || 0) + count) }
  const total = [...categories.values()].reduce((sum, count) => sum + count, 0)
  return [...categories].map(([category, count]) => ({ category, count, rate: total ? count / total : 0 })).sort((a, b) => b.count - a.count)
})
let errorsController: AbortController | null = null
async function loadErrors() {
  errorsController?.abort(); remoteErrors.value = []; errorsLoading.value = false; errorsFailed.value = false
  if (previewMode.value || view.value !== 'advanced' || !['legacy', 'terminal_v1'].includes(overview.value?.source || '')) return
  const controller = new AbortController(); errorsController = controller; errorsLoading.value = true
  const frozen = { ...filter.value, platforms: [...filter.value.platforms], groupIds: [...filter.value.groupIds], models: [...filter.value.models] }
  try { const result = await getErrors(frozen, true, controller.signal); if (!controller.signal.aborted) remoteErrors.value = result.items } catch { if (!controller.signal.aborted) errorsFailed.value = true } finally { if (controller === errorsController) errorsLoading.value = false }
}
function clearFilters() { filter.value = { ...filter.value, platforms: [], groupIds: [], models: [] } }
async function refresh() { if (previewMode.value) return; await load(true, true) }
watch([filter, identity], () => { detail.value = null; remoteErrors.value = []; errorsController?.abort(); if (!previewMode.value) void load() }, { deep: true, flush: 'sync' })
watch([view, overview], () => { if (view.value === 'advanced') void loadErrors(); else { errorsController?.abort(); errorsLoading.value = false } })
watch(previewMode, () => { cancel(); detail.value = null; if (!previewMode.value) void load() })
let timer: ReturnType<typeof setInterval> | undefined
function resume() { if (document.visibilityState === 'visible' && !loading.value) void refresh() }
onMounted(() => { if (!previewMode.value) void load(); timer = setInterval(resume, 60_000); document.addEventListener('visibilitychange', resume) })
onBeforeUnmount(() => { clearInterval(timer); errorsController?.abort(); document.removeEventListener('visibilitychange', resume) })
defineExpose({ refresh, overview })
</script>
