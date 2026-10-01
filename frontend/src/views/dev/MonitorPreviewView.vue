<template>
  <main class="min-h-screen bg-gray-50 px-4 py-6 text-gray-900 dark:bg-dark-950 dark:text-gray-100 sm:px-6 lg:px-10">
    <div class="mx-auto max-w-[1600px] space-y-5">
      <header class="rounded-2xl border border-gray-200 bg-white p-5 shadow-sm dark:border-dark-700 dark:bg-dark-800 sm:p-6">
        <div class="flex flex-wrap items-start justify-between gap-4">
          <div>
            <p class="text-xs font-semibold uppercase tracking-[0.16em] text-primary-600 dark:text-primary-400">{{ t('channelMonitorV2.preview.eyebrow') }}</p>
            <h1 class="mt-1 text-2xl font-semibold tracking-tight">{{ t('channelMonitorV2.preview.title') }}</h1>
            <p class="mt-1 max-w-3xl text-sm text-gray-500 dark:text-gray-400">{{ t('channelMonitorV2.preview.description') }}</p>
          </div>
          <div class="flex flex-wrap items-center gap-2"><button type="button" class="btn btn-secondary btn-sm" data-testid="monitor-preview-theme" @click="toggleTheme">{{ darkTheme ? t('channelMonitorV2.preview.lightTheme') : t('channelMonitorV2.preview.darkTheme') }}</button><span class="badge badge-gray">{{ role === 'admin' ? t('channelMonitorV2.preview.adminView') : t('channelMonitorV2.preview.userView') }}</span></div>
        </div>

        <div class="mt-5 grid gap-3 border-t border-gray-100 pt-4 dark:border-dark-700 lg:grid-cols-[minmax(220px,0.75fr)_minmax(0,1.25fr)]">
          <label class="block text-xs font-medium text-gray-500 dark:text-gray-400">
            {{ t('channelMonitorV2.preview.scenario') }}
            <select v-model="scenarioId" class="input mt-1.5 w-full" data-testid="monitor-preview-scenario">
              <option v-for="item in scenarios" :key="item.id" :value="item.id">{{ scenarioLabel(item.id) }}</option>
            </select>
          </label>
          <div>
            <span class="text-xs font-medium text-gray-500 dark:text-gray-400">{{ t('channelMonitorV2.preview.quickScenarios') }}</span>
            <div class="mt-1.5 flex flex-wrap gap-1.5" role="list" :aria-label="t('channelMonitorV2.preview.quickScenarios')">
              <button
                v-for="item in scenarios"
                :key="item.id"
                type="button"
                role="listitem"
                class="rounded-lg border px-2.5 py-1.5 text-xs transition"
                :class="scenarioId === item.id ? 'border-primary-500 bg-primary-50 text-primary-700 dark:bg-primary-950/40 dark:text-primary-300' : 'border-gray-200 text-gray-600 hover:bg-gray-50 dark:border-dark-600 dark:text-gray-300 dark:hover:bg-dark-700'"
                :aria-pressed="scenarioId === item.id"
                @click="scenarioId = item.id"
              >{{ scenarioLabel(item.id) }}</button>
            </div>
          </div>
        </div>

        <div class="mt-4 flex flex-wrap items-center gap-3">
          <div class="inline-flex rounded-xl border border-gray-200 bg-gray-50 p-1 dark:border-dark-700 dark:bg-dark-900" role="group" :aria-label="t('channelMonitorV2.preview.previewRole')">
            <button type="button" class="rounded-lg px-3 py-1.5 text-xs font-medium" :class="role === 'admin' ? 'bg-white text-gray-900 shadow-sm dark:bg-dark-700 dark:text-white' : 'text-gray-500 dark:text-gray-400'" :aria-pressed="role === 'admin'" @click="role = 'admin'">{{ t('channelMonitorV2.preview.admin') }}</button>
            <button type="button" class="rounded-lg px-3 py-1.5 text-xs font-medium" :class="role === 'user' ? 'bg-white text-gray-900 shadow-sm dark:bg-dark-700 dark:text-white' : 'text-gray-500 dark:text-gray-400'" :aria-pressed="role === 'user'" @click="role = 'user'">{{ t('channelMonitorV2.preview.user') }}</button>
          </div>
          <span class="text-xs text-gray-500 dark:text-gray-400">{{ scenarioDescription(scenario.id) }}</span>
        </div>
      </header>

      <section v-if="isError" class="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-red-200 bg-red-50 p-4 text-sm text-red-800 dark:border-red-900/70 dark:bg-red-950/25 dark:text-red-200" role="alert">
        <span>{{ t('channelMonitorV2.preview.error', { status: scenario.errorStatus || 500 }) }}</span>
        <button type="button" class="btn btn-secondary btn-sm" @click="scenarioId = 'normal'">{{ t('channelMonitorV2.preview.showHealthy') }}</button>
      </section>
      <section v-else-if="isLoading" class="flex items-center gap-3 rounded-xl border border-sky-200 bg-sky-50 p-4 text-sm text-sky-800 dark:border-sky-900/70 dark:bg-sky-950/25 dark:text-sky-200" role="status">
        <span class="h-2.5 w-2.5 animate-pulse rounded-full bg-sky-500" /> {{ t('channelMonitorV2.preview.loading') }}
      </section>

      <AdminMonitorDashboard v-if="overview && role === 'admin'" :key="scenarioId" :preview-overview="overview" preview />
      <template v-else-if="overview">
        <MonitorOverviewHero
          :eyebrow="t('channelMonitorV2.preview.userEyebrow')"
          :title="t('channelMonitorV2.preview.userTitle')"
          :subtitle="t('channelMonitorV2.preview.userSubtitle')"
          :stats="heroStats"
        />
        <MonitorEvidenceStatus :overview="overview" :stale="Boolean(scenario.stale)" />
        <div v-if="overview.coverage.state !== 'complete'" class="flex flex-wrap items-center gap-2 rounded-xl border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-800 dark:border-amber-900/60 dark:bg-amber-950/25 dark:text-amber-200" role="status">
          <span class="badge badge-warning">{{ t('channelMonitorV2.preview.coverage', { state: t(`channelMonitorV2.observation.coverage.${overview.coverage.state}`) }) }}</span>
          <span v-for="reason in overview.coverage.gap_reasons" :key="reason">{{ gapLabel(reason) }}</span>
        </div>

        <MonitorViewSwitcher v-model="view" :label="t('channelMonitorV2.preview.viewLabel')" :items="viewItems" />
        <ObservationCards
          v-if="view === 'cards'"
          :overview="overview"
          :layout="layout"
          :stale="Boolean(scenario.stale)"
          @toggle-layout="layout = layout === 'cards' ? 'list' : 'cards'"
          @detail="openDetail"
          @model="openModel"
          @bucket="openBucket"
        />
        <MonitorHealthMatrix v-else-if="view === 'matrix'" :items="overview.items" :coverage="overview.coverage" :source="overview.source" @detail="openDetail" @model="openModel" @bucket="openBucket" />
        <MonitorTrendPanel v-else :items="overview.items" :coverage="overview.coverage" :source="overview.source" />
        <ObservationDetailDrawer :show="Boolean(detail)" :item="detail?.item || null" :model="detail?.model" :selection="detail?.slot || null" :coverage="overview.coverage" :source="overview.source" preview :stale="Boolean(scenario.stale)" :filter="detailFilter" :identity="'monitor-preview:' + role + ':' + scenarioId" @close="detail = null" />
      </template>
      <section v-else-if="isEmpty" class="rounded-2xl border border-gray-200 bg-white p-12 text-center shadow-sm dark:border-dark-700 dark:bg-dark-800">
        <p class="text-sm font-medium">{{ t('channelMonitorV2.preview.emptyTitle') }}</p>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('channelMonitorV2.preview.emptyDescription') }}</p>
      </section>
    </div>
  </main>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { MonitorFilter, ObservationBucket, ObservationChannel } from '@/api/channelMonitorV2'
import AdminMonitorDashboard from '@/features/channel-monitor-v2/AdminMonitorDashboard.vue'
import MonitorEvidenceStatus from '@/features/channel-monitor-v2/MonitorEvidenceStatus.vue'
import MonitorHealthMatrix from '@/features/channel-monitor-v2/MonitorHealthMatrix.vue'
import MonitorOverviewHero from '@/features/channel-monitor-v2/MonitorOverviewHero.vue'
import MonitorTrendPanel from '@/features/channel-monitor-v2/MonitorTrendPanel.vue'
import MonitorViewSwitcher from '@/features/channel-monitor-v2/MonitorViewSwitcher.vue'
import ObservationCards from '@/features/channel-monitor-v2/ObservationCards.vue'
import ObservationDetailDrawer from '@/features/channel-monitor-v2/ObservationDetailDrawer.vue'
import { summarizeMonitorTraffic } from '@/features/channel-monitor-v2/monitorSummary'
import { getMonitorPreviewScenario, MONITOR_PREVIEW_SCENARIOS, type MonitorPreviewRole, type MonitorPreviewScenarioId } from '@/features/channel-monitor-v2/monitorPreview'

const scenarios = MONITOR_PREVIEW_SCENARIOS
const { t, te, locale } = useI18n()
const scenarioId = ref<MonitorPreviewScenarioId>('normal')
const role = ref<MonitorPreviewRole>('admin')
const view = ref<'cards' | 'matrix' | 'trend'>('cards')
const layout = ref<'cards' | 'list'>('cards')
const darkTheme = ref(false)
function toggleTheme() {
  darkTheme.value = !darkTheme.value
  document.documentElement.classList.toggle('dark', darkTheme.value)
  localStorage.setItem('theme', darkTheme.value ? 'dark' : 'light')
}
onMounted(() => { darkTheme.value = document.documentElement.classList.contains('dark') })
const scenario = computed(() => {
  const result = getMonitorPreviewScenario(scenarioId.value, role.value)
  for (const item of result.overview?.items || []) {
    const key = `channelMonitorV2.preview.channelNames.${item.group_id}`
    if (te(key)) item.group_name = t(key)
  }
  for (const item of result.overview?.dimensions.groups || []) {
    const key = `channelMonitorV2.preview.channelNames.${item.id}`
    if (te(key)) item.name = t(key)
  }
  return result
})
watch(locale, () => { document.title = t('channelMonitorV2.preview.title') }, { immediate: true })
const overview = computed(() => scenario.value.overview)
const isLoading = computed(() => scenario.value.state === 'loading')
const isError = computed(() => scenario.value.state === 'error')
const isEmpty = computed(() => scenario.value.state === 'empty')
const viewItems = computed(() => [{ value: 'cards', label: t('channelMonitorV2.preview.cards') }, { value: 'matrix', label: t('channelMonitorV2.preview.matrix') }, { value: 'trend', label: t('channelMonitorV2.preview.trend') }])
const scenarioLabel = (id: MonitorPreviewScenarioId) => t(`channelMonitorV2.preview.scenarios.${id}.label`)
const scenarioDescription = (id: MonitorPreviewScenarioId) => t(`channelMonitorV2.preview.scenarios.${id}.description`)
const gapLabel = (reason: string) => { const key = `channelMonitorV2.unified.gaps.${reason}`; return te(key) ? t(key) : reason.split('_').join(' ') }
const percent = (value: number | null) => value == null ? '—' : `${Math.round(value * 100)}%`
const latency = (value: number | null) => value == null ? '—' : value < 1000 ? `${Math.round(value)}ms` : `${(value / 1000).toFixed(1)}s`
const detailFilter: MonitorFilter = { range: '24h', platforms: [], groupIds: [], models: [] }
const detail = ref<{ item: ObservationChannel; model?: string; slot: { start: string; bucket: ObservationBucket | null } | null } | null>(null)
function openDetail(item: ObservationChannel) { detail.value = { item, slot: null } }
function openModel(item: ObservationChannel, model: string) { detail.value = { item, model, slot: null } }
function openBucket(item: ObservationChannel, slot: { start: string; bucket: ObservationBucket | null }) { detail.value = { item, slot } }
watch([scenarioId, role], () => { detail.value = null; view.value = 'cards'; layout.value = 'cards' }, { flush: 'sync' })
const heroStats = computed(() => {
  const summary = summarizeMonitorTraffic(overview.value)
  const sources = summary.sources.map(item => item.source).join(' / ')
  return [
    { label: t('channelMonitorV2.dashboard.availableChannels'), value: `${summary.healthy} / ${summary.total}`, detail: t('channelMonitorV2.dashboard.healthBasis'), tone: 'text-emerald-600 dark:text-emerald-400' },
    { label: t('channelMonitorV2.dashboard.healthRate'), value: percent(summary.healthRate), detail: t('channelMonitorV2.dashboard.healthBasis'), tone: 'text-sky-600 dark:text-sky-400' },
    { label: t('channelMonitorV2.dashboard.medianChannelTtft'), value: summary.sources.map(item => latency(item.ttftP50)).join(' / ') || '—', detail: sources || t('channelMonitorV2.evidence.sources.none'), tone: 'text-violet-600 dark:text-violet-400' },
    { label: t('channelMonitorV2.dashboard.errorRate'), value: summary.sources.map(item => percent(item.errorRate)).join(' / ') || '—', detail: sources || t('channelMonitorV2.evidence.sources.none'), tone: 'text-red-600 dark:text-red-400' },
  ]
})
</script>
