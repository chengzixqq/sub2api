<template>
  <div class="flex flex-wrap items-center justify-between gap-3" :class="motionClass">
    <div ref="tablist" class="tabs relative isolate inline-grid max-w-full !gap-0" :style="{ gridTemplateColumns: `repeat(${items.length}, minmax(0, 1fr))` }" role="tablist" :aria-label="label">
      <span v-if="items.length" aria-hidden="true" class="monitor-view-indicator tab-active pointer-events-none absolute bottom-1 left-1 top-1 -z-10 rounded-lg" :style="{ width: `calc((100% - 0.5rem) / ${items.length})`, transform: `translateX(${activeIndex * 100}%)` }" />
      <button v-for="(item, index) in items" :key="item.value" type="button" role="tab" class="tab monitor-view-tab !px-3 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-primary-500" :class="modelValue === item.value ? 'tab-active !bg-transparent !shadow-none' : ''" :aria-selected="modelValue === item.value" :tabindex="index === activeIndex ? 0 : -1" @click="emit('update:modelValue', item.value)" @keydown="handleKeydown($event, index)">{{ item.label }}</button>
    </div>
    <slot />
  </div>
</template>
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useMonitorMotion } from './useMonitorPreferences'

const props = defineProps<{ modelValue: string; label?: string; items: Array<{ value: string; label: string }> }>()
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const motionClass = useMonitorMotion()
const tablist = ref<HTMLElement | null>(null)
const activeIndex = computed(() => Math.max(0, props.items.findIndex(item => item.value === props.modelValue)))

function handleKeydown(event: KeyboardEvent, index: number) {
  const count = props.items.length
  if (!count) return
  const next = event.key === 'ArrowRight' ? (index + 1) % count
    : event.key === 'ArrowLeft' ? (index + count - 1) % count
      : event.key === 'Home' ? 0 : event.key === 'End' ? count - 1 : null
  if (next == null) return
  event.preventDefault()
  emit('update:modelValue', props.items[next].value)
  tablist.value?.querySelectorAll<HTMLButtonElement>('[role="tab"]')[next]?.focus()
}
</script>


<style scoped>
.monitor-view-indicator { transition: transform 220ms cubic-bezier(.2, .8, .2, 1); }
.monitor-view-tab { transition: color 180ms ease, transform 180ms ease; }
.monitor-view-tab:active { transform: scale(.96); }
@media (prefers-reduced-motion: reduce) {
  .monitor-view-tab, .monitor-view-indicator { transition: none; }
  .monitor-view-tab:active { transform: none; }
}
</style>
