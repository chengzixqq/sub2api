<template>
  <div class="flex shrink-0 justify-end border-b border-gray-200 bg-white px-3 py-1.5 dark:border-dark-700 dark:bg-dark-900">
      <button
        type="button"
        class="flex h-8 w-8 items-center justify-center rounded-lg text-gray-500 hover:bg-gray-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary-500 dark:text-dark-400 dark:hover:bg-dark-800"
        :title="t('tableColumns.title')"
        :aria-label="t('tableColumns.title')"
        aria-haspopup="dialog"
        data-test="column-order-toggle"
        @click="show = true"
      >
        <Icon name="swap" size="sm" />
      </button>
    <BaseDialog :show="show" :title="t('tableColumns.title')" width="narrow" :close-on-click-outside="true" @close="show = false">
        <ol class="max-h-72 overflow-y-auto">
          <li v-for="(column, index) in columns" :key="column.key" class="flex min-h-10 items-center gap-2 border-t border-gray-100 py-1 first:border-0 dark:border-dark-700" :data-column-option="column.key">
            <span class="min-w-0 flex-1 break-words text-sm text-gray-700 dark:text-gray-300">{{ column.label }}</span>
            <button type="button" class="btn btn-ghost h-8 w-8 shrink-0 p-0 disabled:opacity-30" :disabled="index === 0" :title="t('tableColumns.moveEarlier', { column: column.label })" :aria-label="t('tableColumns.moveEarlier', { column: column.label })" :data-move-earlier="column.key" @click="emit('move', column.key, -1)">
              <Icon name="arrowUp" size="sm" />
            </button>
            <button type="button" class="btn btn-ghost h-8 w-8 shrink-0 p-0 disabled:opacity-30" :disabled="index === columns.length - 1" :title="t('tableColumns.moveLater', { column: column.label })" :aria-label="t('tableColumns.moveLater', { column: column.label })" :data-move-later="column.key" @click="emit('move', column.key, 1)">
              <Icon name="arrowDown" size="sm" />
            </button>
          </li>
        </ol>
      <template #footer>
        <button type="button" class="btn btn-secondary" data-test="column-order-reset" @click="emit('reset')">
          <Icon name="refresh" size="sm" />
          {{ t('tableColumns.reset') }}
        </button>
      </template>
    </BaseDialog>
  </div>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import BaseDialog from './BaseDialog.vue'
import type { Column } from './types'

defineProps<{ columns: Column[] }>()
const emit = defineEmits<{ move: [key: string, offset: -1 | 1]; reset: [] }>()
const { t } = useI18n()
const show = ref(false)
</script>
