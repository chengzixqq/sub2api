<template>
  <section class="relative overflow-hidden rounded-2xl border border-gray-200 bg-white p-5 shadow-sm dark:border-dark-700 dark:bg-dark-800 sm:p-6">
    <div class="pointer-events-none absolute -right-16 -top-20 h-48 w-48 rounded-full bg-primary-100/60 blur-3xl dark:bg-primary-900/20" />
    <div class="relative flex flex-col gap-5 lg:flex-row lg:items-end lg:justify-between">
      <div>
        <p class="text-xs font-semibold uppercase tracking-[0.16em] text-primary-600 dark:text-primary-400">{{ eyebrow }}</p>
        <h2 class="mt-1 text-2xl font-semibold tracking-tight text-gray-900 dark:text-white">{{ title }}</h2>
        <p class="mt-1 max-w-2xl text-sm text-gray-500 dark:text-gray-400">{{ subtitle }}</p>
      </div>
      <div class="grid grid-cols-2 gap-3 sm:grid-cols-4 lg:min-w-[520px]">
        <div v-for="stat in stats" :key="stat.label" class="rounded-xl border border-gray-100 bg-gray-50/80 p-3 dark:border-dark-700 dark:bg-dark-900/50">
          <p class="text-[11px] text-gray-500 dark:text-gray-400">{{ stat.label }}</p>
          <p class="mt-1 text-xl font-semibold tabular-nums" :class="stat.tone || 'text-gray-900 dark:text-white'">{{ stat.value }}</p>
          <p v-if="stat.detail" class="mt-0.5 break-words text-[10px] text-gray-500 dark:text-gray-400">{{ stat.detail }}</p>
        </div>
      </div>
    </div>
  </section>
</template>
<script setup lang="ts">
defineProps<{ eyebrow?: string; title: string; subtitle: string; stats: Array<{ label: string; value: string | number; detail?: string; tone?: string }> }>()
</script>
