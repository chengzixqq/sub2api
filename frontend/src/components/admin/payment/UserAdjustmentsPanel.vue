<template>
  <div class="space-y-4">
    <div class="card p-4">
      <div class="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-7">
        <div class="sm:col-span-2 xl:col-span-2">
          <label class="input-label">{{ t('payment.admin.adjustments.filters.keyword') }}</label>
          <div class="relative">
            <Icon
              name="search"
              size="md"
              class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-gray-400"
            />
            <input
              v-model.trim="filters.keyword"
              type="text"
              class="input pl-10"
              :placeholder="t('payment.admin.adjustments.filters.keywordPlaceholder')"
              @keyup.enter="search"
            />
          </div>
        </div>

        <div>
          <label class="input-label">{{ t('payment.admin.adjustments.filters.operator') }}</label>
          <input
            v-model.trim="filters.operator"
            type="text"
            class="input"
            :placeholder="t('payment.admin.adjustments.filters.operatorPlaceholder')"
            @keyup.enter="search"
          />
        </div>

        <div>
          <label class="input-label">{{ t('payment.admin.adjustments.filters.kind') }}</label>
          <Select v-model="filters.kind" :options="kindOptions" />
        </div>

        <div>
          <label class="input-label">{{ t('payment.admin.adjustments.filters.operation') }}</label>
          <Select v-model="filters.operation" :options="operationOptions" />
        </div>

        <div>
          <label class="input-label">{{ t('payment.admin.adjustments.filters.direction') }}</label>
          <Select v-model="filters.direction" :options="directionOptions" />
        </div>

        <div>
          <label class="input-label">{{ t('payment.admin.adjustments.filters.startTime') }}</label>
          <input v-model="filters.startTime" type="datetime-local" class="input" @keyup.enter="search" />
        </div>

        <div>
          <label class="input-label">{{ t('payment.admin.adjustments.filters.endTime') }}</label>
          <input v-model="filters.endTime" type="datetime-local" class="input" @keyup.enter="search" />
        </div>

        <div class="flex flex-wrap items-center gap-2 sm:col-span-2 lg:col-span-3 2xl:col-span-7 2xl:justify-end">
          <button type="button" class="btn btn-primary flex-1 sm:flex-none" :disabled="loading" @click="search">
            {{ t('common.search') }}
          </button>
          <button type="button" class="btn btn-secondary flex-1 sm:flex-none" :disabled="loading" @click="resetFilters">
            {{ t('common.reset') }}
          </button>
          <button
            type="button"
            class="btn btn-secondary"
            :disabled="loading"
            :title="t('common.refresh')"
            @click="loadAdjustments"
          >
            <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
          </button>
          <button
            type="button"
            class="btn btn-secondary flex-1 sm:flex-none"
            :disabled="exporting"
            @click="exportAdjustments"
          >
            <Icon name="download" size="sm" />
            {{ exporting ? t('payment.admin.adjustments.exporting') : t('payment.admin.adjustments.exportCsv') }}
          </button>
        </div>
      </div>
    </div>

    <div class="card overflow-hidden">
      <div class="grid grid-cols-2 divide-x divide-y divide-gray-100 dark:divide-dark-700 sm:grid-cols-4 xl:grid-cols-7">
        <div
          v-for="metric in summaryMetrics"
          :key="metric.key"
          class="min-w-0 px-4 py-3 first:border-t-0"
        >
          <p class="truncate text-xs font-medium text-gray-500 dark:text-gray-400" :title="metric.label">
            {{ metric.label }}
          </p>
          <p class="mt-1 truncate text-base font-semibold" :class="metric.tone" :title="metric.value">
            {{ metric.value }}
          </p>
        </div>
      </div>
    </div>

    <DataTable :columns="columns" :data="adjustments" :loading="loading" row-key="id">
      <template #cell-created_at="{ value }">
        <span class="whitespace-nowrap text-xs text-gray-500 dark:text-gray-400">
          {{ formatDateTime(value) }}
        </span>
      </template>

      <template #cell-target="{ row }">
        <div class="min-w-0 max-w-[220px]">
          <div class="truncate font-medium text-gray-900 dark:text-white" :title="userPrimary(row)">
            {{ userPrimary(row) }}
          </div>
          <div class="mt-0.5 truncate text-xs text-gray-400" :title="userSecondary(row)">
            {{ userSecondary(row) }}
          </div>
        </div>
      </template>

      <template #cell-kind="{ row }">
        <div class="flex flex-col items-start gap-1 md:items-start">
          <span :class="kindBadgeClass(row.kind)">{{ kindLabel(row.kind) }}</span>
          <span class="text-xs text-gray-500 dark:text-gray-400">{{ operationLabel(row.operation) }}</span>
        </div>
      </template>

      <template #cell-change="{ row }">
        <div class="whitespace-nowrap">
          <div class="font-mono font-semibold" :class="deltaToneClass(row.delta)">
            {{ formatSignedValue(row.kind, row.delta) }}
          </div>
          <div class="mt-0.5 font-mono text-xs text-gray-400">
            {{ formatValue(row.kind, row.before_value) }} &rarr; {{ formatValue(row.kind, row.after_value) }}
          </div>
        </div>
      </template>

      <template #cell-operator="{ row }">
        <div class="min-w-0 max-w-[200px]">
          <div class="truncate text-sm text-gray-800 dark:text-gray-200" :title="operatorPrimary(row)">
            {{ operatorPrimary(row) }}
          </div>
          <div v-if="operatorSecondary(row)" class="mt-0.5 truncate text-xs text-gray-400" :title="operatorSecondary(row)">
            {{ operatorSecondary(row) }}
          </div>
        </div>
      </template>

      <template #cell-notes="{ row }">
        <span class="block max-w-[240px] truncate text-sm text-gray-600 dark:text-gray-300" :title="row.notes || ''">
          {{ row.notes || t('payment.admin.adjustments.noNotes') }}
        </span>
      </template>

      <template #cell-actions="{ row }">
        <button
          type="button"
          class="inline-flex items-center gap-1 font-medium text-primary-600 transition-colors hover:text-primary-700 dark:text-primary-400 dark:hover:text-primary-300"
          @click="openDetail(row)"
        >
          <Icon name="eye" size="sm" />
          {{ t('common.view') }}
        </button>
      </template>

      <template #empty>
        <div class="flex flex-col items-center py-8">
          <Icon name="inbox" size="xl" class="mb-3 h-12 w-12 text-gray-300 dark:text-dark-600" />
          <p class="text-sm font-medium text-gray-500 dark:text-gray-400">
            {{ t('payment.admin.adjustments.empty') }}
          </p>
        </div>
      </template>
    </DataTable>

    <Pagination
      v-if="pagination.total > 0"
      :page="pagination.page"
      :total="pagination.total"
      :page-size="pagination.pageSize"
      @update:page="handlePageChange"
      @update:pageSize="handlePageSizeChange"
    />

    <BaseDialog
      :show="selectedAdjustment !== null"
      :title="t('payment.admin.adjustments.detail.title')"
      width="wide"
      @close="selectedAdjustment = null"
    >
      <dl v-if="selectedAdjustment" class="grid grid-cols-1 gap-x-6 gap-y-4 sm:grid-cols-2">
        <div>
          <dt class="detail-label">{{ t('payment.admin.adjustments.detail.actionId') }}</dt>
          <dd class="detail-value break-all font-mono text-xs">{{ selectedAdjustment.action_id }}</dd>
        </div>
        <div>
          <dt class="detail-label">{{ t('payment.admin.adjustments.detail.ledgerId') }}</dt>
          <dd class="detail-value font-mono">#{{ selectedAdjustment.id }}</dd>
        </div>
        <div>
          <dt class="detail-label">{{ t('payment.admin.adjustments.columns.time') }}</dt>
          <dd class="detail-value">{{ formatDateTime(selectedAdjustment.created_at) }}</dd>
        </div>
        <div>
          <dt class="detail-label">{{ t('payment.admin.adjustments.columns.target') }}</dt>
          <dd class="detail-value">{{ userPrimary(selectedAdjustment) }} ({{ userSecondary(selectedAdjustment) }})</dd>
        </div>
        <div>
          <dt class="detail-label">{{ t('payment.admin.adjustments.columns.operator') }}</dt>
          <dd class="detail-value">
            {{ operatorPrimary(selectedAdjustment) }}
            <span v-if="operatorSecondary(selectedAdjustment)"> ({{ operatorSecondary(selectedAdjustment) }})</span>
          </dd>
        </div>
        <div>
          <dt class="detail-label">{{ t('payment.admin.adjustments.columns.kind') }}</dt>
          <dd class="detail-value">{{ kindLabel(selectedAdjustment.kind) }} / {{ operationLabel(selectedAdjustment.operation) }}</dd>
        </div>
        <div>
          <dt class="detail-label">{{ t('payment.admin.adjustments.detail.requestedValue') }}</dt>
          <dd class="detail-value font-mono">{{ formatValue(selectedAdjustment.kind, selectedAdjustment.requested_value) }}</dd>
        </div>
        <div>
          <dt class="detail-label">{{ t('payment.admin.adjustments.detail.beforeAfter') }}</dt>
          <dd class="detail-value font-mono">
            {{ formatValue(selectedAdjustment.kind, selectedAdjustment.before_value) }}
            &rarr;
            {{ formatValue(selectedAdjustment.kind, selectedAdjustment.after_value) }}
          </dd>
        </div>
        <div>
          <dt class="detail-label">{{ t('payment.admin.adjustments.columns.change') }}</dt>
          <dd class="detail-value font-mono font-semibold" :class="deltaToneClass(selectedAdjustment.delta)">
            {{ formatSignedValue(selectedAdjustment.kind, selectedAdjustment.delta) }}
          </dd>
        </div>
        <div>
          <dt class="detail-label">{{ t('payment.admin.adjustments.detail.source') }}</dt>
          <dd class="detail-value">{{ sourceLabel(selectedAdjustment.source) }}</dd>
        </div>
        <div>
          <dt class="detail-label">{{ t('payment.admin.adjustments.detail.authMethod') }}</dt>
          <dd class="detail-value">{{ authMethodLabel(selectedAdjustment.auth_method) }}</dd>
        </div>
        <div>
          <dt class="detail-label">{{ t('payment.admin.adjustments.detail.requestId') }}</dt>
          <dd class="detail-value break-all font-mono text-xs">{{ selectedAdjustment.request_id || unknownLabel }}</dd>
        </div>
        <div>
          <dt class="detail-label">{{ t('payment.admin.adjustments.detail.clientIp') }}</dt>
          <dd class="detail-value break-all font-mono">{{ selectedAdjustment.client_ip || unknownLabel }}</dd>
        </div>
        <div v-if="selectedAdjustment.legacy_redeem_code_id" class="sm:col-span-2">
          <dt class="detail-label">{{ t('payment.admin.adjustments.detail.legacyRedeemCodeId') }}</dt>
          <dd class="detail-value font-mono">#{{ selectedAdjustment.legacy_redeem_code_id }}</dd>
        </div>
        <div class="sm:col-span-2">
          <dt class="detail-label">{{ t('payment.admin.adjustments.columns.notes') }}</dt>
          <dd class="detail-value whitespace-pre-wrap break-words">{{ selectedAdjustment.notes || t('payment.admin.adjustments.noNotes') }}</dd>
        </div>
      </dl>
    </BaseDialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { userAdjustmentsAPI } from '@/api/admin/userAdjustments'
import type {
  UserAdjustment,
  UserAdjustmentDirection,
  UserAdjustmentExportQuery,
  UserAdjustmentKind,
  UserAdjustmentOperation,
  UserAdjustmentQuery,
  UserAdjustmentSummary
} from '@/api/admin/userAdjustments'
import type { Column } from '@/components/common/types'
import { useAppStore } from '@/stores/app'
import BaseDialog from '@/components/common/BaseDialog.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'

const { t } = useI18n()
const appStore = useAppStore()

const emptySummary = (): UserAdjustmentSummary => ({
  record_count: '0',
  balance_increase: '0',
  balance_decrease: '0',
  balance_net: '0',
  concurrency_increase: '0',
  concurrency_decrease: '0',
  concurrency_net: '0'
})

const filters = reactive({
  keyword: '',
  operator: '',
  kind: '' as '' | UserAdjustmentKind,
  operation: '' as '' | UserAdjustmentOperation,
  direction: '' as '' | UserAdjustmentDirection,
  startTime: '',
  endTime: ''
})
const adjustments = ref<UserAdjustment[]>([])
const summary = ref<UserAdjustmentSummary>(emptySummary())
const pagination = reactive({ page: 1, pageSize: 20, total: 0 })
const loading = ref(false)
const exporting = ref(false)
const selectedAdjustment = ref<UserAdjustment | null>(null)
let requestSequence = 0

const unknownLabel = computed(() => t('payment.admin.adjustments.unknown'))

const kindOptions = computed(() => [
  { value: '', label: t('payment.admin.adjustments.filters.allKinds') },
  { value: 'balance', label: t('payment.admin.adjustments.kinds.balance') },
  { value: 'concurrency', label: t('payment.admin.adjustments.kinds.concurrency') }
])

const operationOptions = computed(() => [
  { value: '', label: t('payment.admin.adjustments.filters.allOperations') },
  { value: 'add', label: t('payment.admin.adjustments.operations.add') },
  { value: 'subtract', label: t('payment.admin.adjustments.operations.subtract') },
  { value: 'set', label: t('payment.admin.adjustments.operations.set') },
  { value: 'legacy', label: t('payment.admin.adjustments.operations.legacy') }
])

const directionOptions = computed(() => [
  { value: '', label: t('payment.admin.adjustments.filters.allDirections') },
  { value: 'increase', label: t('payment.admin.adjustments.directions.increase') },
  { value: 'decrease', label: t('payment.admin.adjustments.directions.decrease') }
])

const columns = computed<Column[]>(() => [
  { key: 'created_at', label: t('payment.admin.adjustments.columns.time') },
  { key: 'target', label: t('payment.admin.adjustments.columns.target') },
  { key: 'kind', label: t('payment.admin.adjustments.columns.kind') },
  { key: 'change', label: t('payment.admin.adjustments.columns.change') },
  { key: 'operator', label: t('payment.admin.adjustments.columns.operator') },
  { key: 'notes', label: t('payment.admin.adjustments.columns.notes') },
  { key: 'actions', label: t('common.actions') }
])

const summaryMetrics = computed(() => {
  const metrics = [
    {
      key: 'record_count',
      label: t('payment.admin.adjustments.summary.recordCount'),
      value: summary.value.record_count,
      tone: 'text-gray-900 dark:text-white'
    }
  ]
  if (filters.kind !== 'concurrency') {
    metrics.push(
      {
        key: 'balance_increase',
        label: t('payment.admin.adjustments.summary.balanceIncrease'),
        value: forceSignedValue('balance', summary.value.balance_increase, 1),
        tone: 'text-emerald-600 dark:text-emerald-400'
      },
      {
        key: 'balance_decrease',
        label: t('payment.admin.adjustments.summary.balanceDecrease'),
        value: forceSignedValue('balance', summary.value.balance_decrease, -1),
        tone: 'text-red-600 dark:text-red-400'
      },
      {
        key: 'balance_net',
        label: t('payment.admin.adjustments.summary.balanceNet'),
        value: formatSignedValue('balance', summary.value.balance_net),
        tone: deltaToneClass(summary.value.balance_net)
      }
    )
  }
  if (filters.kind !== 'balance') {
    metrics.push(
      {
        key: 'concurrency_increase',
        label: t('payment.admin.adjustments.summary.concurrencyIncrease'),
        value: forceSignedValue('concurrency', summary.value.concurrency_increase, 1),
        tone: 'text-emerald-600 dark:text-emerald-400'
      },
      {
        key: 'concurrency_decrease',
        label: t('payment.admin.adjustments.summary.concurrencyDecrease'),
        value: forceSignedValue('concurrency', summary.value.concurrency_decrease, -1),
        tone: 'text-red-600 dark:text-red-400'
      },
      {
        key: 'concurrency_net',
        label: t('payment.admin.adjustments.summary.concurrencyNet'),
        value: formatSignedValue('concurrency', summary.value.concurrency_net),
        tone: deltaToneClass(summary.value.concurrency_net)
      }
    )
  }
  return metrics
})

function parseDecimal(value: string | null | undefined) {
  const raw = String(value ?? '0').trim()
  const match = raw.match(/^([+-]?)(\d+)(?:\.(\d+))?$/)
  if (!match) return { negative: false, zero: false, absolute: raw }
  const integer = (match[2].replace(/^0+(?=\d)/, '') || '0')
  const fraction = (match[3] || '').replace(/0+$/, '')
  const zero = /^0+$/.test(integer) && fraction.length === 0
  return {
    negative: !zero && match[1] === '-',
    zero,
    absolute: fraction ? `${integer}.${fraction}` : integer
  }
}

function formatAbsolute(kind: UserAdjustmentKind, value: string | null | undefined): string {
  const parsed = parseDecimal(value)
  if (!/^\d+(?:\.\d+)?$/.test(parsed.absolute)) return parsed.absolute
  if (kind === 'concurrency') return parsed.absolute
  const parts = parsed.absolute.split('.')
  const fraction = (parts[1] || '').padEnd(2, '0')
  return `$${parts[0]}.${fraction}`
}

function formatValue(kind: UserAdjustmentKind, value: string | null): string {
  if (value === null || value === undefined || value === '') return unknownLabel.value
  const parsed = parseDecimal(value)
  return `${parsed.negative ? '-' : ''}${formatAbsolute(kind, parsed.absolute)}`
}

function formatSignedValue(kind: UserAdjustmentKind, value: string | null | undefined): string {
  const parsed = parseDecimal(value)
  if (parsed.zero) return formatAbsolute(kind, parsed.absolute)
  return `${parsed.negative ? '-' : '+'}${formatAbsolute(kind, parsed.absolute)}`
}

function forceSignedValue(kind: UserAdjustmentKind, value: string, direction: 1 | -1): string {
  const parsed = parseDecimal(value)
  if (parsed.zero) return formatAbsolute(kind, parsed.absolute)
  return `${direction < 0 ? '-' : '+'}${formatAbsolute(kind, parsed.absolute)}`
}

function deltaToneClass(value: string): string {
  const parsed = parseDecimal(value)
  if (parsed.zero) return 'text-gray-700 dark:text-gray-300'
  return parsed.negative
    ? 'text-red-600 dark:text-red-400'
    : 'text-emerald-600 dark:text-emerald-400'
}

function kindBadgeClass(kind: UserAdjustmentKind): string {
  return kind === 'balance'
    ? 'badge bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
    : 'badge bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
}

function kindLabel(kind: UserAdjustmentKind): string {
  return t(`payment.admin.adjustments.kinds.${kind}`)
}

function operationLabel(operation: UserAdjustmentOperation): string {
  return t(`payment.admin.adjustments.operations.${operation}`)
}

function userPrimary(row: UserAdjustment): string {
  return row.user_email || row.user_name || (row.user_id ? `#${row.user_id}` : unknownLabel.value)
}

function userSecondary(row: UserAdjustment): string {
  const parts: string[] = []
  if (row.user_id) parts.push(`#${row.user_id}`)
  if (row.user_name && row.user_name !== row.user_email) parts.push(row.user_name)
  return parts.join(' / ') || unknownLabel.value
}

function operatorPrimary(row: UserAdjustment): string {
  return row.operator_email || row.operator_name || (row.operator_user_id ? `#${row.operator_user_id}` : t('payment.admin.adjustments.legacyOperator'))
}

function operatorSecondary(row: UserAdjustment): string {
  const parts: string[] = []
  if (row.operator_name && row.operator_name !== row.operator_email) parts.push(row.operator_name)
  if (row.operator_user_id) parts.push(`#${row.operator_user_id}`)
  return parts.join(' / ')
}

function sourceLabel(source: string): string {
  const knownSources: Record<string, string> = {
    admin: 'admin',
    admin_api: 'admin',
    admin_action: 'admin',
    admin_balance: 'adminBalance',
    admin_user_update: 'adminUserUpdate',
    admin_batch_concurrency: 'adminBatchConcurrency',
    admin_batch_limits: 'adminBatchLimits',
    legacy: 'legacy',
    legacy_redeem_code: 'legacy'
  }
  const key = knownSources[source]
  return key ? t(`payment.admin.adjustments.sources.${key}`) : source || unknownLabel.value
}

function authMethodLabel(method: string | null): string {
  if (!method) return unknownLabel.value
  if (method === 'jwt') return t('payment.admin.adjustments.authMethods.jwt')
  if (method === 'admin_api_key') return t('payment.admin.adjustments.authMethods.adminApiKey')
  return method
}

function formatDateTime(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}

function toRFC3339(value: string): string | undefined {
  if (!value) return undefined
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? undefined : date.toISOString()
}

function buildFilterQuery(): UserAdjustmentExportQuery | null {
  const startTime = toRFC3339(filters.startTime)
  const endTime = toRFC3339(filters.endTime)
  if (startTime && endTime && new Date(startTime).getTime() >= new Date(endTime).getTime()) {
    appStore.showError(t('payment.admin.adjustments.errors.invalidTimeRange'))
    return null
  }
  return {
    keyword: filters.keyword || undefined,
    operator: filters.operator || undefined,
    kind: filters.kind || undefined,
    operation: filters.operation || undefined,
    direction: filters.direction || undefined,
    start_time: startTime,
    end_time: endTime
  }
}

async function loadAdjustments() {
  const query = buildFilterQuery()
  if (!query) return
  const sequence = ++requestSequence
  loading.value = true
  try {
    const result = await userAdjustmentsAPI.list({
      ...query,
      page: pagination.page,
      page_size: pagination.pageSize
    } satisfies UserAdjustmentQuery)
    if (sequence !== requestSequence) return
    adjustments.value = result.items || []
    summary.value = { ...emptySummary(), ...(result.summary || {}) }
    pagination.page = result.pagination?.page || pagination.page
    pagination.pageSize = result.pagination?.page_size || pagination.pageSize
    pagination.total = result.pagination?.total || 0
  } catch (error: any) {
    if (sequence !== requestSequence) return
    appStore.showError(error.message || error.response?.data?.detail || t('payment.admin.adjustments.errors.loadFailed'))
  } finally {
    if (sequence === requestSequence) loading.value = false
  }
}

function search() {
  pagination.page = 1
  void loadAdjustments()
}

function resetFilters() {
  filters.keyword = ''
  filters.operator = ''
  filters.kind = ''
  filters.operation = ''
  filters.direction = ''
  filters.startTime = ''
  filters.endTime = ''
  pagination.page = 1
  void loadAdjustments()
}

function handlePageChange(page: number) {
  pagination.page = page
  void loadAdjustments()
}

function handlePageSizeChange(pageSize: number) {
  pagination.pageSize = pageSize
  pagination.page = 1
  void loadAdjustments()
}

function openDetail(adjustment: UserAdjustment) {
  selectedAdjustment.value = adjustment
}

async function exportAdjustments() {
  const query = buildFilterQuery()
  if (!query) return
  exporting.value = true
  try {
    const blob = await userAdjustmentsAPI.exportCSV(query)
    const url = window.URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = `user-adjustments-${new Date().toISOString().replace(/[:T]/g, '-').slice(0, 19)}.csv`
    document.body.appendChild(link)
    link.click()
    link.remove()
    window.URL.revokeObjectURL(url)
    appStore.showSuccess(t('payment.admin.adjustments.exportSuccess'))
  } catch (error: any) {
    appStore.showError(error.message || error.response?.data?.detail || t('payment.admin.adjustments.errors.exportFailed'))
  } finally {
    exporting.value = false
  }
}

onMounted(() => {
  void loadAdjustments()
})
</script>

<style scoped>
.detail-label {
  @apply text-xs font-medium text-gray-500 dark:text-gray-400;
}

.detail-value {
  @apply mt-1 text-sm text-gray-900 dark:text-gray-100;
}
</style>
