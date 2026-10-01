<template>
  <section class="rounded-2xl border border-gray-200 bg-white p-5 shadow-sm dark:border-dark-700 dark:bg-dark-800">
    <header class="flex items-start justify-between gap-3"><div><h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ text('错误分类', 'Error breakdown') }}</h3><p class="mt-1 text-xs text-gray-500">{{ text('仅展示已分类错误；比例以本面板分类总数为分母。', 'Classified errors only; shares use the category total in this panel.') }}</p></div><span class="badge badge-gray">{{ sourceLabel }}</span></header>
    <p v-if="loading" class="py-10 text-center text-sm text-gray-500">{{ text('正在加载错误分类…', 'Loading error categories…') }}</p>
    <div v-else-if="failed" role="alert" class="flex items-center justify-between gap-3 py-5 text-sm text-amber-700"><span>{{ text('错误分类暂时不可用。', 'Error categories are unavailable.') }}</span><button type="button" class="btn btn-secondary btn-sm" @click="$emit('retry')">{{ text('重试', 'Retry') }}</button></div>
    <p v-else-if="!items.length" class="py-10 text-center text-sm text-gray-500">{{ text('当前范围没有已分类错误，不能据此推断错误率为零。', 'No classified errors in this range; this does not imply a zero error rate.') }}</p>
    <div v-else class="mt-4 space-y-3"><div v-for="item in items" :key="item.category" class="space-y-1"><div class="flex items-center justify-between gap-3 text-xs"><span class="font-medium text-gray-700 dark:text-gray-200">{{ categoryLabel(item.category) }}<span v-if="item.ignored" class="ml-2 rounded bg-gray-100 px-1.5 py-0.5 text-[10px] text-gray-500 dark:bg-dark-700">{{ text('忽略', 'ignored') }}</span></span><span class="tabular-nums text-gray-500">{{ item.count }} · {{ share(item.count).toFixed(1) }}%</span></div><div class="h-1.5 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700"><div class="h-full rounded-full bg-red-400" :style="{ width: share(item.count) + '%' }" /></div></div></div>
  </section>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { monitorCategoryLabel, monitorSourceLabel } from './monitorLabels'
import type { MonitorErrorRow } from '@/api/channelMonitorV2'
const props = defineProps<{ items: MonitorErrorRow[]; source?: string; loading?: boolean; failed?: boolean }>()
defineEmits<{ retry: [] }>()
const { t, te, locale } = useI18n()
const text = (zh: string, en: string) => locale.value.startsWith('zh') ? zh : en
const categoryLabel = (category: string) => monitorCategoryLabel(category, t, te)
const sourceLabel = computed(() => monitorSourceLabel(props.source, t, te))
const total = computed(() => props.items.reduce((sum, item) => sum + item.count, 0))
const share = (count: number) => total.value ? count / total.value * 100 : 0
</script>
