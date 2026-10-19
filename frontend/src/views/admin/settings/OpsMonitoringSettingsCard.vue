<template>
  <div class="card" data-testid="ops-monitoring-settings">
    <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
        {{ t('admin.settings.opsMonitoring.title') }}
      </h2>
      <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
        {{ t('admin.settings.opsMonitoring.description') }}
      </p>
    </div>
    <div class="space-y-5 p-6">
      <div class="flex items-center justify-between gap-4">
        <div class="min-w-0">
          <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
            {{ t('admin.settings.opsMonitoring.enabled') }}
          </label>
          <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.settings.opsMonitoring.enabledHint') }}
          </p>
          <p
            v-if="configDisabled"
            class="mt-1.5 text-xs text-amber-600 dark:text-amber-400"
            data-testid="ops-monitoring-config-disabled-hint"
          >
            {{ t('admin.settings.opsMonitoring.configDisabledHint') }}
          </p>
        </div>
        <Toggle
          data-testid="ops-monitoring-enabled-toggle"
          :model-value="effectiveEnabled"
          :disabled="configDisabled"
          @update:model-value="emit('update:enabled', $event)"
        />
      </div>

      <div v-if="effectiveEnabled" class="space-y-5">
        <div class="flex items-center justify-between gap-4">
          <div class="min-w-0">
            <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t('admin.settings.opsMonitoring.realtimeEnabled') }}
            </label>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.settings.opsMonitoring.realtimeEnabledHint') }}
            </p>
          </div>
          <Toggle
            data-testid="ops-realtime-monitoring-toggle"
            :model-value="realtimeEnabled"
            @update:model-value="emit('update:realtimeEnabled', $event)"
          />
        </div>

        <div data-testid="ops-query-mode-select" class="max-w-md">
          <label class="input-label">
            {{ t('admin.settings.opsMonitoring.queryMode') }}
          </label>
          <Select
            :model-value="queryMode"
            :options="queryModeOptions"
            @update:model-value="emit('update:queryMode', String($event ?? 'auto'))"
          />
          <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.settings.opsMonitoring.queryModeHint') }}
          </p>
        </div>

        <div class="max-w-md">
          <label class="input-label" for="ops-metrics-interval-seconds">
            {{ t('admin.settings.opsMonitoring.metricsInterval') }}
          </label>
          <input
            id="ops-metrics-interval-seconds"
            data-testid="ops-metrics-interval-input"
            type="number"
            :min="OPS_METRICS_INTERVAL_MIN_SECONDS"
            :max="OPS_METRICS_INTERVAL_MAX_SECONDS"
            class="input"
            :value="metricsIntervalSeconds"
            @input="onIntervalInput"
          />
          <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.settings.opsMonitoring.metricsIntervalHint') }}
          </p>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import Toggle from '@/components/common/Toggle.vue'
import {
  OPS_METRICS_INTERVAL_MAX_SECONDS,
  OPS_METRICS_INTERVAL_MIN_SECONDS,
} from './opsMonitoringSettings'

const props = defineProps<{
  enabled: boolean
  realtimeEnabled: boolean
  queryMode: string
  metricsIntervalSeconds: number
  /** Server config hard switch; null while unknown (treated as on). */
  configEnabled: boolean | null
}>()

const emit = defineEmits<{
  (e: 'update:enabled', value: boolean): void
  (e: 'update:realtimeEnabled', value: boolean): void
  (e: 'update:queryMode', value: string): void
  (e: 'update:metricsIntervalSeconds', value: number): void
}>()

const { t } = useI18n()

const configDisabled = computed(() => props.configEnabled === false)
// With the config switch off, ops stays off whatever the setting says.
const effectiveEnabled = computed(() => props.enabled && !configDisabled.value)

const queryModeOptions = computed(() => [
  { value: 'auto', label: t('admin.settings.opsMonitoring.queryModeAuto') },
  { value: 'raw', label: t('admin.settings.opsMonitoring.queryModeRaw') },
  { value: 'preagg', label: t('admin.settings.opsMonitoring.queryModePreagg') },
])

function onIntervalInput(event: Event) {
  emit('update:metricsIntervalSeconds', Number((event.target as HTMLInputElement).value))
}
</script>
