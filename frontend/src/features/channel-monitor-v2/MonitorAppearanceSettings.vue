<template>
  <section class="overflow-hidden rounded-xl border border-gray-200 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-800" :class="motionClass">
    <button type="button" class="flex w-full items-center justify-between gap-3 px-4 py-3 text-left text-sm font-medium text-gray-700 transition-colors hover:bg-gray-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-primary-500 dark:text-gray-200 dark:hover:bg-dark-700/50" :aria-expanded="expanded" :aria-controls="panelId" @click="expanded = !expanded" data-testid="pref-disclosure">
      <span>{{ siteDefaults ? text('全站显示默认值', 'Site display defaults') : text('显示偏好', 'Display preferences') }}</span>
      <Icon name="chevronDown" size="sm" class="text-gray-400 transition-transform duration-200" :class="{ 'rotate-180': expanded }" aria-hidden="true" />
    </button>
    <Transition name="monitor-preferences-panel">
      <div v-show="expanded" :id="panelId" class="border-t border-gray-100 px-4 pb-4 dark:border-dark-700">
        <p class="mt-3 text-xs text-gray-500">{{ siteDefaults ? text('保存后作为所有用户的默认外观；用户已自定义的字段继续保留。', 'Saved values become the defaults for all users. Fields customized by a user remain unchanged.') : text('未自定义的字段跟随管理员默认值；个人修改保存在此浏览器，按账号和工作区隔离。', 'Unmodified fields follow administrator defaults. Personal changes are saved in this browser per account and workspace.') }}</p>
        <div class="mt-4 grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          <label class="space-y-1 text-xs"><span>{{ text('卡片密度', 'Card density') }}</span><Select :disabled="disabled || !expanded" :dropdown-class="motionClass" v-model="preferences.density" :options="[{ value: 'compact', label: text('紧凑', 'Compact') }, { value: 'comfortable', label: text('宽松', 'Comfortable') }]" :searchable="false" :aria-label="text('卡片密度', 'Card density')" /></label>
          <label class="space-y-1 text-xs"><span>{{ text('宽屏最大列数', 'Maximum wide-screen columns') }}</span><Select :disabled="disabled || !expanded" :dropdown-class="motionClass" v-model="preferences.columns" :options="columnOptions" :searchable="false" :aria-label="text('宽屏最大列数', 'Maximum wide-screen columns')" /></label>
          <label class="space-y-1 text-xs"><span>{{ text('动态效果', 'Motion') }}</span><Select :disabled="disabled || !expanded" :dropdown-class="motionClass" v-model="preferences.motion" :options="[{ value: 'system', label: text('跟随系统', 'Follow system') }, { value: 'off', label: text('关闭动画', 'Off') }]" :searchable="false" :aria-label="text('动态效果', 'Motion')" /></label>
          <label class="space-y-1 text-xs"><span>{{ text('默认视图', 'Default view') }}</span><Select :disabled="disabled || !expanded" :dropdown-class="motionClass" v-model="preferences.defaultView" :options="viewOptions" :searchable="false" :aria-label="text('默认视图', 'Default view')" /></label>
          <label class="space-y-1 text-xs"><span>{{ text('默认时间范围', 'Default time range') }}</span><Select :disabled="disabled || !expanded" :dropdown-class="motionClass" v-model="preferences.defaultRange" :options="rangeOptions" :searchable="false" :aria-label="text('默认时间范围', 'Default time range')" /></label>
        </div>
        <form class="mt-4 space-y-3 border-t border-gray-100 pt-4 dark:border-dark-700" @submit.prevent="applyHistory">
          <div>
            <h3 class="text-xs font-semibold">{{ text('历史状态条', 'History bars') }}</h3>
            <p class="mt-1 text-xs text-gray-500">{{ text('每根状态条汇总对应时段内的真实样本；这里只调整历史聚合区间，不改变采集、探测或刷新频率。', 'Each bar summarizes real samples within its time window. This changes history aggregation, not collection, probe, or refresh frequency.') }}</p>
          </div>
          <div class="grid items-end gap-3 sm:grid-cols-2 lg:grid-cols-3">
            <label class="space-y-1 text-xs"><span>{{ text('历史显示方式', 'History display mode') }}</span><Select :disabled="disabled || !expanded" v-model="historyDraft.mode" :dropdown-class="motionClass" :options="historyOptions" :searchable="false" :aria-label="text('历史显示方式', 'History display mode')" data-testid="pref-history-mode" /></label>
            <label v-if="historyDraft.mode === 'interval'" class="space-y-1 text-xs"><span>{{ text('每条汇总区间（分钟）', 'Aggregation interval per bar (minutes)') }}</span><input :disabled="disabled" v-model.number="historyDraft.minutes" class="input" type="number" min="1" max="43200" step="1" :aria-invalid="Boolean(historyError)" :aria-describedby="historyHintId" data-testid="pref-history-minutes" /></label>
            <label v-else-if="historyDraft.mode === 'count'" class="space-y-1 text-xs"><span>{{ text('目标状态条数量', 'Target bar count') }}</span><input :disabled="disabled" v-model.number="historyDraft.count" class="input" type="number" min="1" max="720" step="1" :aria-invalid="Boolean(historyError)" :aria-describedby="historyHintId" data-testid="pref-history-count" /></label>
            <button type="submit" class="btn btn-secondary btn-sm justify-self-start" :disabled="disabled || Boolean(historyError) || !historyDirty" data-testid="pref-history-apply">{{ text('应用历史设置', 'Apply history settings') }}</button>
          </div>
          <div :id="historyHintId" class="space-y-1 text-xs text-gray-500">
            <p v-if="historyError" role="alert" class="text-red-600 dark:text-red-400">{{ historyError }}</p>
            <template v-else>
              <p v-if="historyDraft.mode === 'auto'">{{ text('自动：90 分钟 / 24 小时使用 5 分钟区间；7 天 / 30 天使用 60 分钟区间。', 'Auto: 5-minute intervals for 90m / 24h, and 60-minute intervals for 7d / 30d.') }}</p>
              <p v-else-if="historyDraft.mode === 'interval'">{{ text('期望区间：', 'Desired interval: ') }}{{ historyDraft.minutes }}{{ text(' 分钟；当前范围将请求：', ' min; this range requests: ') }}{{ draftResolution.bucketMinutes }}{{ text(' 分钟 / 条。跨范围时向上对齐可用粒度并限制在时间范围内，原偏好不变。', ' min per bar. Range switches round up to the supported granularity and cap at the range, without changing your preference.') }}</p>
              <p v-else>{{ text('目标 ', 'Target: ') }}{{ historyDraft.count }}{{ text(' 条；当前范围将按 ', ' bars; this range aggregates ') }}{{ draftResolution.bucketMinutes }}{{ text(' 分钟汇总。实际条数受 1 / 60 分钟最小粒度、时段对齐及首尾区间影响，不保证等于目标。', ' minutes per bar. Actual counts depend on 1 / 60-minute minimum granularity, alignment and partial edge windows; the target is not an exact count.') }}</p>
            </template>
            <p v-if="bucketSeconds && bucketSeconds > 0" data-testid="pref-history-loaded">{{ text('当前已加载：每条 ', 'Currently loaded: ') }}{{ bucketSeconds / 60 }}{{ text(' 分钟；实际起止时间与样本以状态条悬浮提示为准。', ' minutes; hover a bar for its actual sample window.') }}</p>
          </div>
          <p v-if="historyFeedback" role="status" class="text-xs text-emerald-700 dark:text-emerald-300">{{ historyFeedback }}</p>
        </form>
        <div class="mt-4 flex flex-wrap gap-x-6 gap-y-3 text-xs">
          <label class="inline-flex items-center gap-2"><Toggle v-model="preferences.metricDetails" :disabled="disabled" :aria-label="text('显示指标 P95 详情', 'Show metric P95 details')" data-testid="pref-metric-details" />{{ text('显示指标 P95 详情', 'Show metric P95 details') }}</label>
          <label class="inline-flex items-center gap-2"><Toggle v-model="preferences.expandModels" :disabled="disabled" :aria-label="text('默认展开模型', 'Expand models by default')" data-testid="pref-expand-models" />{{ text('默认展开模型', 'Expand models by default') }}</label>
          <label class="inline-flex items-center gap-2"><Toggle v-model="preferences.autoRefresh" :disabled="disabled" :aria-label="text('自动刷新（使用站点周期）', 'Auto-refresh using site interval')" data-testid="pref-auto-refresh" />{{ text('自动刷新（使用站点周期）', 'Auto-refresh using site interval') }}</label>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="disabled" @click="resetPreferences" data-testid="pref-reset">{{ siteDefaults ? text('恢复已加载的默认值', 'Reset loaded defaults') : text('恢复管理员默认值', 'Reset to administrator defaults') }}</button>
        </div>
        <p v-if="preview" class="mt-3 text-xs text-amber-700 dark:text-amber-300">{{ text('验收页按所选时间范围生成示例数据，不请求线上数据；自动刷新仅在真实页面生效。', 'The preview generates fixtures for the selected range without live requests. Auto-refresh only runs on live pages.') }}</p>
        <slot />
      </div>
    </Transition>
  </section>
</template>
<script setup lang="ts">
import { computed, getCurrentInstance, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import Toggle from '@/components/common/Toggle.vue'
import Icon from '@/components/icons/Icon.vue'
import type { MonitorRange } from '@/api/channelMonitorV2'
import { isHistoryBarCount, isHistoryBucketMinutes, resolveMonitorHistory, type MonitorHistoryPreferences } from './monitorHistoryPreferences'
import { useMonitorMotion, useMonitorPreferences } from './useMonitorPreferences'
const props = defineProps<{ siteDefaults?: boolean; preview?: boolean; range?: MonitorRange; bucketSeconds?: number; disabled?: boolean }>()
const { t, locale } = useI18n()
const text = (zh: string, en: string) => locale.value.startsWith('zh') ? zh : en
const preferences = useMonitorPreferences()
const resetPreferences = () => preferences.reset()
const motionClass = useMonitorMotion()
const expanded = ref(false)
const panelId = 'monitor-preferences-panel-' + getCurrentInstance()?.uid
const historyHintId = 'monitor-history-hint-' + getCurrentInstance()?.uid
const historyDraft = reactive<{ mode: MonitorHistoryPreferences['historyMode']; minutes: number | string; count: number | string }>({ mode: preferences.value.historyMode, minutes: preferences.value.historyBucketMinutes, count: preferences.value.historyBarCount })
const historyFeedback = ref('')
watch(historyDraft, () => { historyFeedback.value = '' }, { flush: 'sync' })
watch(() => [preferences.value, preferences.value.historyMode, preferences.value.historyBucketMinutes, preferences.value.historyBarCount] as const, (current, previous) => {
  const draftWasDirty = historyDraft.mode !== previous[1]
    || (historyDraft.mode === 'interval' && historyDraft.minutes !== previous[2])
    || (historyDraft.mode === 'count' && historyDraft.count !== previous[3])
  // Compare against the old defaults: newly received defaults are not user edits.
  if (preferences.applyingDefaults.value && (draftWasDirty || current.slice(1).every((value, index) => value === previous[index + 1]))) return
  historyDraft.mode = preferences.value.historyMode
  historyDraft.minutes = preferences.value.historyBucketMinutes
  historyDraft.count = preferences.value.historyBarCount
  historyFeedback.value = ''
}, { flush: 'sync' })
const historyOptions = computed(() => [{ value: 'auto', label: text('自动（默认）', 'Auto (default)') }, { value: 'interval', label: text('按分钟区间', 'By interval') }, { value: 'count', label: text('按目标条数', 'By target count') }])
const historyError = computed(() => historyDraft.mode === 'interval' && !isHistoryBucketMinutes(historyDraft.minutes)
  ? text('请输入 1–43200 之间的整数分钟。', 'Enter an integer from 1 to 43200 minutes.')
  : historyDraft.mode === 'count' && !isHistoryBarCount(historyDraft.count)
    ? text('请输入 1–720 之间的整数条数。', 'Enter an integer bar count from 1 to 720.') : '')
const draftResolution = computed(() => resolveMonitorHistory(props.range || preferences.value.defaultRange, {
  historyMode: historyDraft.mode,
  historyBucketMinutes: isHistoryBucketMinutes(historyDraft.minutes) ? historyDraft.minutes : preferences.value.historyBucketMinutes,
  historyBarCount: isHistoryBarCount(historyDraft.count) ? historyDraft.count : preferences.value.historyBarCount,
}))
const historyDirty = computed(() => historyDraft.mode !== preferences.value.historyMode || (historyDraft.mode === 'interval' && historyDraft.minutes !== preferences.value.historyBucketMinutes) || (historyDraft.mode === 'count' && historyDraft.count !== preferences.value.historyBarCount))
function applyHistory() {
  if (props.disabled || historyError.value || !historyDirty.value) return
  preferences.value = {
    ...preferences.value,
    historyMode: historyDraft.mode,
    historyBucketMinutes: historyDraft.mode === 'interval' && isHistoryBucketMinutes(historyDraft.minutes) ? historyDraft.minutes : preferences.value.historyBucketMinutes,
    historyBarCount: historyDraft.mode === 'count' && isHistoryBarCount(historyDraft.count) ? historyDraft.count : preferences.value.historyBarCount,
  }
  historyFeedback.value = props.siteDefaults ? text('历史设置已应用到草稿，请保存全站默认值。', 'History settings applied to the draft. Save site defaults to publish.') : text('历史设置已应用并保存在此浏览器。', 'History settings applied and saved in this browser.')
}
const columnOptions = computed(() => [{ value: 'auto', label: text('自动', 'Auto') }, ...['2', '3', '4'].map(value => ({ value, label: value }))])
const viewOptions = computed(() => [{ value: 'cards', label: text('卡片', 'Cards') }, { value: 'matrix', label: text('矩阵', 'Matrix') }, { value: 'trend', label: text('趋势 / 管理端高级大屏', 'Trends / admin advanced view') }])
const rangeOptions = computed(() => ['90m', '24h', '7d', '30d'].map(value => ({ value, label: t('channelMonitorV2.ranges.' + value) })))
</script>

<style scoped>
.monitor-preferences-panel-enter-active,
.monitor-preferences-panel-leave-active {
  transition: opacity 180ms ease, transform 180ms ease;
}
.monitor-preferences-panel-enter-from,
.monitor-preferences-panel-leave-to {
  opacity: 0;
  transform: translateY(-6px);
}
</style>
