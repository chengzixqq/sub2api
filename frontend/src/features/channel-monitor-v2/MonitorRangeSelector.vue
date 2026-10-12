<template>
  <div class="tabs inline-flex w-auto shrink-0" role="group" :aria-label="t('channelMonitorV2.timeRange')" data-testid="monitor-range-selector">
    <button v-for="range in ranges" :key="range" type="button" class="tab !px-3 !py-2 text-xs" :class="modelValue === range ? 'tab-active' : ''" :aria-pressed="modelValue === range" @click="emit('update:modelValue', range)">{{ t(`channelMonitorV2.ranges.${range}`) }}</button>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { MonitorRange } from '@/api/channelMonitorV2'
defineProps<{ modelValue: MonitorRange }>()
const emit = defineEmits<{ 'update:modelValue': [range: MonitorRange] }>()
const { t } = useI18n()
const ranges: MonitorRange[] = ['90m', '24h', '7d', '30d']
</script>
