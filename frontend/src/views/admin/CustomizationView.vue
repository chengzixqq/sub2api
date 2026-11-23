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
        <button v-if="corruptSettings" type="button" class="btn btn-secondary" :disabled="busy" data-reset-all @click="resetAll">
          {{ t('admin.customization.resetCorruptSettings') }}
        </button>
        <button type="button" class="btn btn-secondary" :disabled="busy" data-testid="customization-retry" @click="load">
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
        <div v-for="field in invalidFields" :key="field" class="flex items-center gap-3 px-4 py-2 text-sm" role="alert">
          {{ t('admin.customization.invalidField', { field: fieldLabel(field) }) }}
          <button type="button" class="btn btn-secondary" :disabled="busy" :data-reset-field="field" @click="resetField(field)">
            {{ t('admin.customization.resetField') }}
          </button>
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
          <OfficialAlignmentCard
            v-if="alignmentFields.length" mode="global" class="px-4 py-5 sm:px-6"
            :fields="alignmentFields" :model-value="alignmentValues" :hits="hits" :disabled="busy"
            @update:model-value="updateAlignment"
          />
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
import OfficialAlignmentCard from '@/components/admin/customization/OfficialAlignmentCard.vue'
import Select from '@/components/common/Select.vue'
import Toggle from '@/components/common/Toggle.vue'
import Icon from '@/components/icons/Icon.vue'
import {
  resetClaudeCustomization, resetClaudeCustomizationField, applyClaudeCustomizationPreset, getClaudeCustomization, getClaudeCustomizationSchema, updateClaudeCustomization,
  type AlignmentHitCounts, type ClaudeCustomizationField, type ClaudeCustomizationSettings,
} from '@/api/admin/settings'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'

const { t, te } = useI18n()
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
  thinking_signature_retry_enabled: false, thinking_tool_downgrade_retry_enabled: false,
  beta_policy_mode: 'capability_aware', unknown_beta_action: 'pass_on_native_only',
  fingerprint_unification: true, metadata_passthrough: true, url_redaction_enabled: true,
  context_management_missing_beta: 'strip', request_id_mode: 'passthrough', cache_control_limit_mode: 'trim',
  unknown_beta_header: 'passthrough', models_catalog: 'synthetic', upstream_header_passthrough: 'off',
  thinking_cache_control: 'strip', bedrock_cc_context_management: 'strip',
})
const corruptSettings = ref(false)
const invalidFields = ref<string[]>([])
const alignmentFields = ref<ClaudeCustomizationField[]>([])
const hits = ref<Record<string, AlignmentHitCounts> | null>()
const alignmentValues = computed<Record<string, string>>(() => Object.fromEntries(
  alignmentFields.value.flatMap(({ key }) => {
    const value = form[key]
    return typeof value === 'string' ? [[key, value]] : []
  })
))
const editedAlignmentFields = new Set<ClaudeCustomizationField['key']>()
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

function updateAlignment(values: Record<string, string | null>) {
  for (const { key } of alignmentFields.value) {
    const value = values[key]
    if (typeof value === 'string' && value !== alignmentValues.value[key]) {
      Object.assign(form, { [key]: value })
      editedAlignmentFields.add(key)
    }
  }
  message.value = ''
}

function replaceSettings(settings: ClaudeCustomizationSettings) {
  Object.assign(form, settings, Object.fromEntries(
    alignmentFields.value.map(({ key }) => [key, settings[key]])
  ))
  editedAlignmentFields.clear()
}

async function load() {
  corruptSettings.value = false
  loading.value = true
  try {
    const [settings, schema] = await Promise.all([getClaudeCustomization(), getClaudeCustomizationSchema()])
    alignmentFields.value = schema.filter(field => field.group === 'official_alignment')
    replaceSettings(settings.global)
    invalidFields.value = settings.invalid_fields ?? []
    hits.value = settings.hits
    loaded.value = true
  } catch (error) {
    corruptSettings.value = !!error && typeof error === 'object' && 'reason' in error && error.reason === 'CLAUDE_CUSTOMIZATION_CORRUPT'
    loaded.value = false
    appStore.showError(extractApiErrorMessage(error, t('common.error')))
  } finally {
    loading.value = false
  }
}
function fieldLabel(field: string) {
  if (field === 'preset') return t('admin.customization.presetLabel')
  const prefix = alignmentFields.value.some(f => f.key === field) ? 'alignment.fields' : 'fields'
  const path = `admin.customization.${prefix}.${field}.label`
  return te(path) ? t(path) : field
}
async function resetAll() {
  if (!corruptSettings.value || busy.value) return
  saving.value = true
  try {
    await resetClaudeCustomization()
    await load()
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('common.error')))
  } finally {
    saving.value = false
  }
}

async function resetField(field: string) {
  if (busy.value) return
  saving.value = true
  try {
    await resetClaudeCustomizationField(field)
    await load()
    if (loaded.value) message.value = t('common.saved')
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('common.error')))
  } finally {
    saving.value = false
  }
}
async function applyPreset(value: unknown) {
  if (busy.value || !loaded.value || !presets.includes(value as typeof presets[number])) return
  applyingPreset.value = true
  message.value = ''
  try {
    replaceSettings((await applyClaudeCustomizationPreset(value as ClaudeCustomizationSettings['preset'])).global)
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
    const payload = {
      ...form,
      // Empty alignment values ask the server to preserve its current settings.
      ...Object.fromEntries(alignmentFields.value.map(({ key }) => [
        key, editedAlignmentFields.has(key) ? form[key] : '',
      ])),
    }
    replaceSettings((await updateClaudeCustomization(payload)).global)
    message.value = t('common.saved')
  } catch (error) {
    appStore.showError(extractApiErrorMessage(error, t('common.error')))
  } finally {
    saving.value = false
  }
}
onMounted(load)
</script>
