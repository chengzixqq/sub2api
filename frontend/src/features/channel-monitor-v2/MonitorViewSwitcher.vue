<template>
  <div class="flex flex-wrap items-center justify-between gap-3">
    <div class="inline-flex rounded-xl border border-gray-200 bg-white p-1 dark:border-dark-700 dark:bg-dark-800" role="tablist" :aria-label="label">
      <button v-for="item in items" :key="item.value" type="button" role="tab" class="rounded-lg px-3 py-1.5 text-xs font-medium transition" :class="modelValue === item.value ? 'bg-gray-900 text-white shadow-sm dark:bg-white dark:text-gray-900' : 'text-gray-500 hover:bg-gray-100 dark:text-gray-400 dark:hover:bg-dark-700'" :aria-selected="modelValue === item.value" @click="$emit('update:modelValue', item.value)">{{ item.label }}</button>
    </div>
    <slot />
  </div>
</template>
<script setup lang="ts">
defineProps<{ modelValue: string; label?: string; items: Array<{ value: string; label: string }> }>()
defineEmits<{ 'update:modelValue': [value: string] }>()
</script>
