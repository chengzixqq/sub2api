<template>
  <div
    class="flex max-w-full gap-1 overflow-x-auto border-b border-gray-100 px-2 py-1.5 dark:border-dark-700"
    role="group"
    :aria-label="t('keys.platformFilter')"
    @keydown.enter.stop
    @keydown.space.stop
  >
    <button
      v-for="platform in platforms"
      :key="platform.value"
      type="button"
      :data-platform="platform.value"
      :aria-pressed="modelValue === platform.value"
      class="shrink-0 whitespace-nowrap rounded-md px-2.5 py-1.5 text-xs font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500"
      :class="modelValue === platform.value
        ? 'bg-primary-50 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300'
        : 'text-gray-600 hover:bg-gray-100 dark:text-dark-300 dark:hover:bg-dark-700'"
      @click.stop="$emit('update:modelValue', platform.value)"
    >
      {{ platform.label }}
    </button>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { GROUP_PLATFORM_OPTIONS } from '@/constants/platforms'

const props = defineProps<{
  modelValue: string
  options: ReadonlyArray<{ [key: string]: unknown }>
}>()
defineEmits<{ 'update:modelValue': [value: string] }>()
const { t } = useI18n()
const platforms = computed(() => {
  const available = new Set(props.options.map(option => option.platform))
  return [
    { value: '', label: t('keys.allPlatforms') },
    ...GROUP_PLATFORM_OPTIONS.filter(platform => available.has(platform.value)),
  ]
})
</script>
