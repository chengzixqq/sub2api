<template>
  <AppLayout>
    <div class="mx-auto max-w-5xl space-y-6 p-4 md:p-6">
      <div>
        <h1 class="text-2xl font-semibold text-gray-900 dark:text-white">{{ t('admin.customization.title') }}</h1>
        <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.customization.description') }}</p>
      </div>
      <section class="rounded-lg border border-gray-200 bg-white p-5 shadow-sm dark:border-gray-700 dark:bg-gray-800">
        <div class="mb-5 flex flex-wrap gap-2">
          <button v-for="preset in presets" :key="preset" class="btn" :class="form.preset === preset ? 'btn-primary' : 'btn-secondary'" @click="applyPreset(preset)">{{ t(`admin.customization.presets.${preset}`) }}</button>
        </div>
        <div class="grid gap-5 md:grid-cols-2">
          <label v-for="field in fields" :key="field.key" class="block">
            <span class="text-sm font-medium text-gray-800 dark:text-gray-100">{{ t(`admin.customization.fields.${field.key}.label`) }}</span>
            <select v-if="field.type === 'select'" v-model="form[field.key]" class="input mt-1 w-full">
              <option v-for="option in field.options" :key="option" :value="option">{{ t(`admin.customization.options.${option}`) }}</option>
            </select>
            <input v-else v-model="form[field.key]" type="checkbox" class="mt-2 h-4 w-4" />
            <span class="mt-1 block text-xs text-gray-500 dark:text-gray-400">{{ t(`admin.customization.fields.${field.key}.hint`) }}</span>
          </label>
        </div>
        <div class="mt-6 flex items-center gap-3">
          <button class="btn btn-primary" :disabled="saving" @click="save">{{ saving ? t('common.saving') : t('common.save') }}</button>
          <span v-if="message" class="text-sm text-green-600">{{ message }}</span>
        </div>
      </section>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import { applyClaudeCustomizationPreset, getClaudeCustomization, updateClaudeCustomization, type ClaudeCustomizationSettings } from '@/api/admin/settings'

const { t } = useI18n()
const presets = ['magic', 'official', 'custom'] as const
const fields = [
  { key: 'fallback_policy', type: 'select', options: ['native_passthrough', 'strict', 'fable_native_passthrough'] },
  { key: 'beta_policy_mode', type: 'select', options: ['capability_aware', 'official_strict', 'client_passthrough'] },
  { key: 'unknown_beta_action', type: 'select', options: ['pass_on_native_only', 'filter', 'pass'] },
  { key: 'thinking_prefilter_enabled', type: 'bool' },
  { key: 'thinking_signature_retry_enabled', type: 'bool' },
  { key: 'thinking_tool_downgrade_retry_enabled', type: 'bool' },
  { key: 'fingerprint_unification', type: 'bool' },
  { key: 'metadata_passthrough', type: 'bool' },
  { key: 'url_redaction_enabled', type: 'bool' },
] as const
const form = reactive<ClaudeCustomizationSettings>({
  preset: 'magic', fallback_policy: 'native_passthrough', thinking_prefilter_enabled: false,
  thinking_signature_retry_enabled: true, thinking_tool_downgrade_retry_enabled: true,
  beta_policy_mode: 'capability_aware', unknown_beta_action: 'pass_on_native_only',
  fingerprint_unification: true, metadata_passthrough: true, url_redaction_enabled: true,
})
const saving = ref(false)
const message = ref('')
async function load() { Object.assign(form, (await getClaudeCustomization()).global) }
async function applyPreset(preset: ClaudeCustomizationSettings['preset']) { Object.assign(form, (await applyClaudeCustomizationPreset(preset)).global) }
async function save() { saving.value = true; message.value = ''; try { Object.assign(form, (await updateClaudeCustomization(form)).global); message.value = t('common.saved') } finally { saving.value = false } }
onMounted(load)
</script>
