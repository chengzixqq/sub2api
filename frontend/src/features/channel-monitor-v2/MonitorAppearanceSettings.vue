<template>
  <details class="rounded-xl border border-gray-200 bg-white p-3 dark:border-dark-700 dark:bg-dark-800" :class="motionClass">
    <summary class="cursor-pointer text-sm font-medium">{{ text('显示偏好', 'Display preferences') }}</summary>
    <p class="mt-3 text-xs text-gray-500">{{ text('自动保存在此浏览器，按账号和工作区隔离；不会更改站点采集与健康判定。', 'Saved in this browser per account and workspace; collection and health policies are unchanged.') }}</p>
    <div class="mt-4 grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
      <label class="space-y-1 text-xs"><span>{{ text('卡片密度', 'Card density') }}</span><Select :dropdown-class="motionClass" v-model="preferences.density" :options="[{ value: 'compact', label: text('紧凑', 'Compact') }, { value: 'comfortable', label: text('宽松', 'Comfortable') }]" :searchable="false" :aria-label="text('卡片密度', 'Card density')" /></label>
      <label class="space-y-1 text-xs"><span>{{ text('宽屏最大列数', 'Maximum wide-screen columns') }}</span><Select :dropdown-class="motionClass" v-model="preferences.columns" :options="columnOptions" :searchable="false" :aria-label="text('宽屏最大列数', 'Maximum wide-screen columns')" /></label>
      <label class="space-y-1 text-xs"><span>{{ text('动态效果', 'Motion') }}</span><Select :dropdown-class="motionClass" v-model="preferences.motion" :options="[{ value: 'system', label: text('跟随系统', 'Follow system') }, { value: 'off', label: text('关闭动画', 'Off') }]" :searchable="false" :aria-label="text('动态效果', 'Motion')" /></label>
      <label class="space-y-1 text-xs"><span>{{ text('默认视图', 'Default view') }}</span><Select :dropdown-class="motionClass" v-model="preferences.defaultView" :options="viewOptions" :searchable="false" :aria-label="text('默认视图', 'Default view')" /></label>
      <label class="space-y-1 text-xs"><span>{{ text('默认时间范围', 'Default time range') }}</span><Select :dropdown-class="motionClass" v-model="preferences.defaultRange" :options="rangeOptions" :searchable="false" :aria-label="text('默认时间范围', 'Default time range')" /></label>
    </div>
    <div class="mt-4 flex flex-wrap gap-x-6 gap-y-3 text-xs">
      <label class="inline-flex items-center gap-2"><input v-model="preferences.metricDetails" type="checkbox" class="checkbox" data-testid="pref-metric-details" />{{ text('显示指标 P95 详情', 'Show metric P95 details') }}</label>
      <label class="inline-flex items-center gap-2"><input v-model="preferences.expandModels" type="checkbox" class="checkbox" data-testid="pref-expand-models" />{{ text('默认展开模型', 'Expand models by default') }}</label>
      <label class="inline-flex items-center gap-2"><input v-model="preferences.autoRefresh" type="checkbox" class="checkbox" data-testid="pref-auto-refresh" />{{ text('自动刷新（使用站点周期）', 'Auto-refresh using site interval') }}</label>
      <button type="button" class="text-primary-600 hover:underline" @click="preferences = { ...DEFAULT_MONITOR_PREFERENCES }">{{ text('恢复显示默认值', 'Reset display preferences') }}</button>
    </div>
    <p v-if="preview" class="mt-3 text-xs text-amber-700 dark:text-amber-300">{{ text('验收页采用固定时间范围的示例数据；默认时间范围和自动刷新将在真实页面生效。', 'The preview uses a fixed fixture window. Default range and auto-refresh take effect on live pages.') }}</p>
  </details>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import { DEFAULT_MONITOR_PREFERENCES, useMonitorMotion, useMonitorPreferences } from './useMonitorPreferences'
defineProps<{ preview?: boolean }>()
const { t, locale } = useI18n()
const text = (zh: string, en: string) => locale.value.startsWith('zh') ? zh : en
const preferences = useMonitorPreferences()
const motionClass = useMonitorMotion()
const columnOptions = computed(() => [{ value: 'auto', label: text('自动', 'Auto') }, ...['2', '3', '4'].map(value => ({ value, label: value }))])
const viewOptions = computed(() => [{ value: 'cards', label: text('卡片', 'Cards') }, { value: 'matrix', label: text('矩阵', 'Matrix') }, { value: 'trend', label: text('趋势 / 管理端高级大屏', 'Trends / admin advanced view') }])
const rangeOptions = computed(() => ['90m', '24h', '7d', '30d'].map(value => ({ value, label: t('channelMonitorV2.ranges.' + value) })))
</script>
