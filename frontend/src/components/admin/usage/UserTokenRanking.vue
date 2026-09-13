<template>
  <!-- 用量页"用户排行"tab 内容：无卡片外观，依赖父级统一卡片；筛选/时间范围复用页面级筛选栏 -->
  <div>
    <UsageRegionState :error="error" @retry="load(true)" />
    <!-- Toolbar -->
    <div class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 px-4 py-3 dark:border-dark-700/50 sm:px-6">
      <p class="text-xs text-gray-400 dark:text-gray-500">{{ t('admin.usage.tokenRanking.subtitle') }}</p>
      <div class="flex items-center gap-3">
        <div class="w-36 md:hidden">
          <Select v-model="sortBy" :options="sortOptions" :aria-label="t('usage.rankingSort')" @change="load()" />
        </div>
        <span v-if="!loading && items.length > 0" class="text-xs text-gray-400 dark:text-gray-500">
          {{ t('admin.usage.tokenRanking.userCount', { count: items.length }) }}
        </span>
        <div class="w-28">
          <Select v-model="limit" :options="limitOptions" @change="load()" />
        </div>
      </div>
    </div>

    <!-- Table -->
    <DataTable
      column-order-key="admin.usage.ranking"
      :columns="columns"
      :data="rankedItems"
      :loading="loading"
      row-key="user_id"
      clickable-rows
      @row-click="$emit('select-user', $event.user_id, $event.email)"
    >
      <template v-for="col in sortableColumns" :key="col.key" #[`header-${col.key}`]>
        <button
          type="button"
          class="inline-flex items-center gap-1 text-xs font-medium"
          :class="sortBy === col.key ? 'text-primary-600 dark:text-primary-400' : 'text-gray-500 dark:text-dark-400'"
          @click.stop="setSort(col.key)"
        >
          {{ t(col.label) }}
          <Icon v-if="sortBy === col.key" name="arrowDown" size="sm" />
        </button>
      </template>
      <template #cell-rank="{ value }">
        <span v-if="value <= 3" class="inline-flex h-6 w-6 items-center justify-center rounded-full text-xs font-semibold" :class="RANK_BADGE_CLASSES[value - 1]">{{ value }}</span>
        <span v-else class="inline-block w-6 text-center text-sm tabular-nums text-gray-400">{{ value }}</span>
      </template>
      <template #cell-email="{ row }">
        <span class="block max-w-[260px] truncate font-medium" :title="row.email">
          {{ row.email || `#${row.user_id}` }}
          <span class="ml-1 font-normal text-gray-400">#{{ row.user_id }}</span>
        </span>
      </template>
      <template #cell-actual_cost="{ value }"><span class="font-medium text-green-600 dark:text-green-400">${{ fmtCost(value) }}</span></template>
      <template #empty>{{ t('admin.dashboard.noDataAvailable') }}</template>
    </DataTable>
  </div>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getUserBreakdown, type UserBreakdownParams } from '@/api/admin/dashboard'
import { formatCompactNumber, formatCostFixed } from '@/utils/format'
import type { UserBreakdownItem } from '@/types'
import Select from '@/components/common/Select.vue'
import DataTable from '@/components/common/DataTable.vue'
import type { Column } from '@/components/common/types'
import Icon from '@/components/icons/Icon.vue'
import UsageRegionState from '@/components/common/UsageRegionState.vue'
import { createUsageRequests } from '@/utils/usageQuery'

const props = defineProps<{
  startDate: string
  endDate: string
  filters: Record<string, unknown>
  model?: string
}>()

defineEmits<{ (e: 'select-user', userId: number, email: string): void }>()

const { t } = useI18n()

type SortKey = NonNullable<UserBreakdownParams['sort_by']>
const sortableColumns: { key: SortKey; label: string }[] = [
  { key: 'requests', label: 'admin.usage.tokenRanking.columns.requests' },
  { key: 'input_tokens', label: 'admin.usage.tokenRanking.columns.inputTokens' },
  { key: 'output_tokens', label: 'admin.usage.tokenRanking.columns.outputTokens' },
  { key: 'cache_tokens', label: 'admin.usage.tokenRanking.columns.cacheTokens' },
  { key: 'total_tokens', label: 'admin.usage.tokenRanking.columns.totalTokens' },
  { key: 'actual_cost', label: 'admin.usage.tokenRanking.columns.cost' },
]

const limitOptions = computed(() => [20, 50, 100, 200].map(value => ({ value, label: t('usage.rankingTop', { count: value }) })))
const sortOptions = computed(() => sortableColumns.map(column => ({ value: column.key, label: t(column.label) })))

// 前三名金/银/铜徽章
const RANK_BADGE_CLASSES = [
  'bg-amber-100 text-amber-700 dark:bg-amber-500/20 dark:text-amber-400',
  'bg-gray-200 text-gray-600 dark:bg-gray-500/20 dark:text-gray-300',
  'bg-orange-100 text-orange-700 dark:bg-orange-500/20 dark:text-orange-400',
]

const items = ref<UserBreakdownItem[]>([])
const loading = ref(false)
const sortBy = ref<SortKey>('total_tokens')
const limit = ref(50)
const requests = createUsageRequests()
const error = ref(false)

const fmtTokens = (v: number) => formatCompactNumber(v)
const fmtCost = (v: number) => formatCostFixed(v, 4)
const rankedItems = computed(() => items.value.map((item, index) => ({ ...item, rank: index + 1 })))
const columns = computed<Column[]>(() => [
  { key: 'rank', label: '#' },
  { key: 'email', label: t('admin.usage.tokenRanking.columns.user') },
  ...sortableColumns.map(column => ({
    key: column.key,
    label: t(column.label),
    class: 'text-right tabular-nums',
    formatter: column.key === 'requests' ? (value: number) => value.toLocaleString() : fmtTokens,
  })),
])

const setSort = (key: SortKey) => {
  if (sortBy.value === key) return
  sortBy.value = key
  load()
}

const load = async (force = false) => {
  const request = requests.start('ranking')
  loading.value = true
  error.value = false
  items.value = []
  try {
    const params: UserBreakdownParams = {
      ...props.filters,
      start_date: props.filters.start_time ? undefined : props.startDate,
      end_date: props.filters.end_time ? undefined : props.endDate,
      sort_by: sortBy.value,
      limit: limit.value,
      force_refresh: force,
    }
    if (props.model) params.model = props.model
    const res = await getUserBreakdown(params, { signal: request.signal })
    if (!request.current()) return
    items.value = res.users || []
  } catch {
    if (!request.current()) return
    error.value = true
    items.value = []
  } finally {
    if (request.current()) loading.value = false
  }
}

// Reload when the shared filters / date range / model change.
watch(
  () => [props.startDate, props.endDate, props.model, JSON.stringify(props.filters)],
  () => load(),
  { immediate: true, flush: 'sync' }
)
onUnmounted(() => requests.cancelAll())

defineExpose({ reload: load })
</script>
