<template>
  <section class="space-y-4">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div class="tabs inline-flex" role="group" :aria-label="t('channelMonitorV2.timeRange')">
        <button v-for="option in ranges" :key="option.value" type="button" class="tab !px-3 !py-1.5 text-xs" :class="range === option.value ? 'tab-active' : ''" @click="setRange(option.value)">
          {{ option.label }}
        </button>
      </div>
      <button type="button" class="btn btn-secondary btn-sm" :disabled="loading" @click="load(true)">
        <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
        {{ t('common.refresh') }}
      </button>
    </div>
    <ObservationCards :overview="overview" :layout="layout" admin :loading="loading" :error="error" @retry="() => load(true)" @toggle-layout="layout = layout === 'cards' ? 'matrix' : 'cards'" />
  </section>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { getObservationOverview, type MonitorFilter, type MonitorRange, type ObservationOverview as Overview } from '@/api/channelMonitorV2'
import ObservationCards from './ObservationCards.vue'
import type { ObservationLayout } from './observationViewModel'

const { t } = useI18n()
const range = ref<MonitorRange>('24h')
const layout = ref<ObservationLayout>('matrix')
const overview = ref<Overview | null>(null)
const loading = ref(false)
const error = ref(false)
let controller: AbortController | null = null

const ranges = [
  { value: '90m' as const, label: '90m' },
  { value: '24h' as const, label: '24h' },
  { value: '7d' as const, label: '7d' },
  { value: '30d' as const, label: '30d' },
]

async function load(refresh = false) {
  controller?.abort()
  const current = new AbortController()
  controller = current
  loading.value = true
  error.value = false
  const filter: MonitorFilter = { range: range.value, platforms: [], groupIds: [], models: [] }
  try {
    overview.value = await getObservationOverview(filter, true, current.signal, { endTime: new Date().toISOString(), refresh })
  } catch (cause) {
    if ((cause as { name?: string }).name !== 'CanceledError' && !current.signal.aborted) {
      overview.value = null
      error.value = true
    }
  } finally {
    if (controller === current) loading.value = false
  }
}
function setRange(value: MonitorRange) {
  if (range.value === value) return
  range.value = value
  void load()
}
onMounted(() => { void load() })
onBeforeUnmount(() => controller?.abort())
</script>
