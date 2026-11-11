<template>
  <div class="space-y-4" data-testid="usage-billing-details">
    <p class="text-sm text-gray-600 dark:text-gray-300">{{ t('usage.billingDetailsNote') }}</p>
    <p class="text-sm font-medium" :class="details.status === 'complete' ? 'text-green-600' : 'text-amber-600'">
      {{ t('usage.billingDetailsStatus') }}: {{ t(`usage.billingStatus.${details.status}`) }}
    </p>
    <div class="max-h-[65vh] space-y-4 overflow-y-auto">
      <section
        v-for="(component, index) in details.components"
        :key="index"
        class="rounded-lg border border-gray-200 p-4 dark:border-gray-700"
        data-testid="billing-component"
      >
        <div class="flex flex-wrap items-center gap-2 text-sm font-semibold">
          <span>{{ t(`usage.billingRole.${component.role}`) }}</span>
          <code class="break-all">{{ component.model || '—' }}</code>
          <span v-if="component.iteration_index != null" class="text-xs font-normal text-gray-500">
            {{ t('usage.billingIteration', { index: component.iteration_index }) }}
          </span>
          <span class="text-xs font-normal">{{ getBillingModeLabel(component.billing_mode, t) }}</span>
        </div>
        <dl class="mt-3 grid grid-cols-2 gap-x-4 gap-y-2 text-xs sm:grid-cols-3">
          <div v-for="field in tokenFields" :key="field.key">
            <dt class="text-gray-500 dark:text-gray-400">{{ t(field.label) }}</dt>
            <dd class="mt-0.5 tabular-nums">{{ (component[field.key] ?? 0).toLocaleString() }}</dd>
          </div>
        </dl>
        <dl class="mt-3 grid grid-cols-2 gap-x-4 gap-y-2 border-t border-gray-100 pt-3 text-xs dark:border-gray-700 sm:grid-cols-3">
          <div v-for="field in costFields" :key="field.key">
            <dt class="text-gray-500 dark:text-gray-400">{{ t(field.label) }}</dt>
            <dd class="mt-0.5 tabular-nums">{{ formatCost(component[field.key]) }}</dd>
          </div>
          <template v-if="showAccountBilling">
            <div>
              <dt class="text-gray-500 dark:text-gray-400">{{ t('usage.billingAccountStatsCost') }}</dt>
              <dd class="mt-0.5 tabular-nums">{{ formatCost(component.account_stats_cost) }}</dd>
            </div>
            <div>
              <dt class="text-gray-500 dark:text-gray-400">{{ t('usage.billingPricingSource') }}</dt>
              <dd class="mt-0.5">{{ component.pricing_source || '—' }}</dd>
            </div>
          </template>
        </dl>
        <p v-if="component.unpriced_reason" class="mt-3 text-xs text-amber-600">
          {{ t('usage.billingUnpriced') }}
        </p>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { getBillingModeLabel } from '@/utils/billingMode'
import type { AdminUsageBillingDetails } from '@/types'

defineProps<{ details: AdminUsageBillingDetails; showAccountBilling: boolean }>()
const { t } = useI18n()
const formatCost = (cost: number | null | undefined): string =>
  cost == null ? t('usage.billingNotPriced') : `$${cost.toFixed(8)}`
const tokenFields = [
  { key: 'input_tokens', label: 'admin.usage.inputTokens' },
  { key: 'output_tokens', label: 'admin.usage.outputTokens' },
  { key: 'cache_creation_tokens', label: 'admin.usage.cacheCreationTokens' },
  { key: 'cache_read_tokens', label: 'admin.usage.cacheReadTokens' },
  { key: 'cache_creation_5m_tokens', label: 'usage.billingCache5m' },
  { key: 'cache_creation_1h_tokens', label: 'usage.billingCache1h' },
  { key: 'cache_creation_30m_tokens', label: 'usage.billingCache30m' },
  { key: 'cache_creation_12h_tokens', label: 'usage.billingCache12h' },
] as const
const costFields = [
  { key: 'input_cost', label: 'admin.usage.inputCost' },
  { key: 'output_cost', label: 'admin.usage.outputCost' },
  { key: 'cache_creation_cost', label: 'admin.usage.cacheCreationCost' },
  { key: 'cache_read_cost', label: 'admin.usage.cacheReadCost' },
  { key: 'total_cost', label: 'usage.original' },
  { key: 'actual_cost', label: 'usage.userBilled' },
] as const
</script>
