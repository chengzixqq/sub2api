<template>
  <div class="flex flex-wrap gap-1.5">
    <span v-if="state !== 'sufficient'" class="badge" :class="state === 'no_samples' ? 'badge-gray' : 'badge-warning'">{{ t(`channelMonitorV2.observation.states.${state}`) }}</span>
    <template v-else>
      <span class="badge" :class="health.reliability === 'healthy' ? 'badge-success' : health.reliability === 'critical' ? 'badge-danger' : 'badge-warning'">{{ t(`channelMonitorV2.observation.states.${health.reliability}`) }}</span>
      <span v-if="health.latency === 'warning' || health.latency === 'critical'" class="badge badge-warning">{{ t('channelMonitorV2.observation.slow') }}</span>
    </template>
  </div>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ObservationHealth, ObservationMetrics, ObservationOverview } from '@/api/channelMonitorV2'
import { observationStatus } from './observationViewModel'
const props = defineProps<{ metrics: ObservationMetrics; health: ObservationHealth; coverage: ObservationOverview['coverage'] }>()
const { t } = useI18n()
const state = computed(() => observationStatus(props.metrics, props.coverage.state))
</script>
