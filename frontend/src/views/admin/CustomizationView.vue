<template>
  <AppLayout>
    <div class="mx-auto max-w-6xl space-y-6">
      <div v-if="loading" class="flex items-center justify-center gap-3 py-12 text-sm text-gray-500 dark:text-gray-400" role="status">
        <Icon name="refresh" size="md" class="animate-spin text-primary-600" />
        {{ t('common.loading') }}
      </div>
      <div v-else-if="!loaded" class="flex flex-col items-center gap-4 py-12" role="alert">
        <Icon name="exclamationTriangle" size="lg" class="text-amber-500" />
        <p class="text-sm text-gray-600 dark:text-gray-400">{{ t('admin.customization.loadFailed') }}</p>
        <button type="button" class="btn btn-secondary" data-testid="customization-retry" @click="load">
          <Icon name="refresh" size="sm" />
          {{ t('common.tryAgain') }}
        </button>
      </div>
      <form v-else class="card overflow-hidden" :aria-busy="busy" @submit.prevent="save">
        <div class="border-b border-gray-100 px-4 py-4 dark:border-dark-700 sm:px-6">
          <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('admin.customization.title') }}</h2>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.customization.description') }}</p>
        </div>
        <div class="flex flex-col gap-3 border-b border-gray-100 px-4 py-4 dark:border-dark-700 sm:flex-row sm:items-center sm:justify-between sm:gap-6 sm:px-6">
          <label for="customization-preset" class="text-sm font-medium text-gray-700 dark:text-gray-300">
            {{ t('admin.customization.presetLabel') }}
          </label>
          <Select
            id="customization-preset"
            :model-value="form.preset"
            :options="presetOptions"
            :disabled="busy"
            :aria-label="t('admin.customization.presetLabel')"
            class="w-full sm:w-72 sm:shrink-0 lg:w-80"
            @update:model-value="applyPreset"
          />
        </div>
        <div class="divide-y divide-gray-100 dark:divide-dark-700">
          <section class="px-4 py-5 sm:px-6" aria-labelledby="customization-compatibility">
            <h3 id="customization-compatibility" class="mb-1 flex items-center gap-2 text-sm font-semibold text-gray-900 dark:text-white">
              <Icon name="server" size="sm" class="text-primary-500" />
              {{ t('admin.customization.sections.compatibility') }}
            </h3>
            <div class="divide-y divide-gray-100 dark:divide-dark-700/50">
              <div v-for="field in selectFields" :key="field.key" class="flex flex-col gap-3 py-4 sm:flex-row sm:items-center sm:justify-between sm:gap-6">
                <div class="min-w-0 flex-1">
                  <label :for="`customization-${field.key}`" class="text-sm font-medium text-gray-700 dark:text-gray-300">
                    {{ t(`admin.customization.fields.${field.key}.label`) }}
                  </label>
                  <p :id="`customization-${field.key}-hint`" class="mt-1 text-xs leading-5 text-gray-500 dark:text-gray-400">
                    {{ t(`admin.customization.fields.${field.key}.hint`) }}
                  </p>
                </div>
                <Select
                  :id="`customization-${field.key}`"
                  v-model="form[field.key]"
                  :options="field.options.map(option => ({ value: option, label: t(`admin.customization.options.${option}`) }))"
                  :disabled="busy"
                  :aria-label="t(`admin.customization.fields.${field.key}.label`)"
                  :aria-describedby="`customization-${field.key}-hint`"
                  class="w-full sm:w-72 sm:shrink-0 lg:w-80"
                  @update:model-value="markCustom"
                />
              </div>
            </div>
          </section>
          <section v-for="group in toggleGroups" :key="group.key" class="px-4 py-5 sm:px-6" :aria-labelledby="`customization-${group.key}`">
            <h3 :id="`customization-${group.key}`" class="mb-1 flex items-center gap-2 text-sm font-semibold text-gray-900 dark:text-white">
              <Icon :name="group.icon" size="sm" class="text-primary-500" />
              {{ t(`admin.customization.sections.${group.key}`) }}
            </h3>
            <div class="divide-y divide-gray-100 dark:divide-dark-700/50">
              <div v-for="key in group.fields" :key="key" class="flex items-start justify-between gap-6 py-4">
                <div class="min-w-0 flex-1">
                  <label :for="`customization-${key}`" class="text-sm font-medium text-gray-700 dark:text-gray-300">
                    {{ t(`admin.customization.fields.${key}.label`) }}
                  </label>
                  <p :id="`customization-${key}-hint`" class="mt-1 text-xs leading-5 text-gray-500 dark:text-gray-400">
                    {{ t(`admin.customization.fields.${key}.hint`) }}
                  </p>
                </div>
                <Toggle
                  :id="`customization-${key}`"
                  v-model="form[key]"
                  :disabled="busy"
                  :aria-label="t(`admin.customization.fields.${key}.label`)"
                  :aria-describedby="`customization-${key}-hint`"
                  class="mt-0.5"
                  @update:model-value="markCustom"
                />
              </div>
            </div>
          </section>
        </div>
        <div class="flex flex-col items-stretch gap-3 border-t border-gray-100 px-4 py-4 dark:border-dark-700 sm:flex-row sm:items-center sm:justify-end sm:px-6">
          <span v-if="message" class="inline-flex items-center gap-1.5 text-sm text-emerald-600 dark:text-emerald-400" role="status">
            <Icon name="check" size="sm" />
            {{ message }}
          </span>
          <button type="submit" class="btn btn-primary" :disabled="busy">
            <Icon :name="saving ? 'refresh' : 'check'" size="sm" :class="saving && 'animate-spin'" />
            {{ saving ? t('common.saving') : t('common.save') }}
          </button>
        </div>
      </form>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Select from '@/components/common/Select.vue'
import Toggle from '@/components/common/Toggle.vue'
import Icon from '@/components/icons/Icon.vue'
import { applyClaudeCustomizationPreset, getClaudeCustomization, updateClaudeCustomization, type ClaudeCustomizationSettings } from '@/api/admin/settings'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'

const { t } = useI18n()
const appStore = useAppStore()
const presets = ['magic', 'official', 'custom'] as const
const presetOptions = computed(() => presets.map(value => ({ value, label: t(`admin.customization.presets.${value}`) })))
const selectFields = [
  { key: 'fallback_policy', options: ['native_passthrough', 'strict', 'fable_native_passthrough'] },
  { key: 'beta_policy_mode', options: ['capability_aware', 'official_strict', 'client_passthrough'] },
  { key: 'unknown_beta_action', options: ['pass_on_native_only', 'filter', 'pass'] },
] as const
const toggleGroups = [
  { key: 'thinking', icon: 'sparkles', fields: ['thinking_prefilter_enabled', 'thinking_signature_retry_enabled', 'thinking_tool_downgrade_retry_enabled'] },
  { key: 'privacy', icon: 'shield', fields: ['fingerprint_unification', 'metadata_passthrough', 'url_redaction_enabled'] },
] as const
const form = reactive<ClaudeCustomizationSettings>({
  preset: 'magic', fallback_policy: 'native_passthrough', thinking_prefilter_enabled: false,
  thinking_signature_retry_enabled: true, thinking_tool_downgrade_retry_enabled: true,
  beta_policy_mode: 'capability_aware', unknown_beta_action: 'pass_on_native_only',
  fingerprint_unification: true, metadata_passthrough: true, url_redaction_enabled: true,
})
const loading = ref(true)
const loaded = ref(false)
const saving = ref(false)
const applyingPreset = ref(false)
const busy = computed(() => loading.value || saving.value || applyingPreset.value)
const message = ref('')

function markCustom() {
  form.preset = 'custom'
  message.value = ''
}

async function load() {
  loading.value = true
  try {
    Object.assign(form, (await getClaudeCustomization()).global)
    loaded.value = true
  } catch (error) {
    loaded.value = false
    appStore.showError(extractApiErrorMessage(error, t('common.error')))
  } finally {
    loading.value = false
  }
}
async function applyPreset(value: unknown) {
  if (busy.value || !loaded.value || !presets.includes(value as typeof presets[number])) return
  applyingPreset.value = true
  message.value = ''
  try {
    Object.assign(form, (await applyClaudeCustomizationPreset(value as ClaudeCustomizationSettings['preset'])).global)
    message.value = t('common.saved')
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('common.error')))
  } finally {
    applyingPreset.value = false
  }
}
async function save() {
  if (busy.value || !loaded.value) return
  saving.value = true
  message.value = ''
  try {
    Object.assign(form, (await updateClaudeCustomization({ ...form })).global)
    message.value = t('common.saved')
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('common.error')))
  } finally {
    saving.value = false
  }
}
onMounted(load)
</script>
