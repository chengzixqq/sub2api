<template>
  <div v-if="loading || error" class="flex min-h-12 items-center justify-center gap-3 py-3 text-sm text-gray-500 dark:text-gray-400" :role="error ? 'alert' : 'status'">
    <LoadingSpinner v-if="loading" size="sm" />
    <span>{{ error ? t('usage.queryFailed') : refreshing ? t('usage.queryRefreshing') : t('common.loading') }}</span>
    <button v-if="error && !loading" type="button" class="btn btn-secondary btn-sm" @click="$emit('retry')">
      <Icon name="refresh" size="sm" />{{ t('common.retry') }}
    </button>
  </div>
</template>
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import Icon from '@/components/icons/Icon.vue'
defineProps<{ loading?: boolean; error?: boolean; refreshing?: boolean }>()
defineEmits<{ retry: [] }>()
const { t } = useI18n()
</script>
