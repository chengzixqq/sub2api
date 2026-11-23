<template>
  <section :aria-labelledby="id + '-title'" class="min-w-0">
    <h3 :id="id + '-title'" class="text-sm font-semibold text-gray-900 dark:text-white">
      {{ t('admin.customization.alignment.title') }}
    </h3>
    <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.customization.alignment.description') }}</p>

    <div class="mt-2 divide-y divide-gray-100 dark:divide-dark-700">
      <div
        v-for="field in fields" :key="field.key" :data-field="field.key"
        class="space-y-2 py-4" :class="{ 'opacity-60': accountLocked(field) }"
      >
        <div class="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between sm:gap-6">
          <div class="min-w-0 flex-1">
            <div class="flex flex-wrap items-center gap-2">
              <label :for="id + field.key" class="text-sm font-medium text-gray-700 dark:text-gray-300">
                {{ fieldText(field, 'label', field.key) }}
              </label>
              <span class="rounded px-2 py-0.5 text-xs font-medium" :class="riskClasses[field.risk] ?? riskClasses.none">
                {{ te('admin.customization.alignment.riskLabels.' + field.risk) ? t('admin.customization.alignment.riskLabels.' + field.risk) : field.risk }}
              </span>
            </div>
            <p :id="id + field.key + '-hint'" class="mt-1 break-words text-xs leading-5 text-gray-500 dark:text-gray-400">
              {{ fieldText(field, 'hint') }}
            </p>
            <p v-if="accountLocked(field)" :id="id + field.key + '-locked'" class="mt-1 text-xs font-medium text-gray-700 dark:text-gray-300">
              {{ t('admin.customization.alignment.' + (field.global_only ? 'globalOnlyHint' : typeLocked(field) ? 'accountTypeHint' : 'notOverridableHint')) }}
              <span v-if="modelValue[field.key] != null">{{ t('admin.customization.alignment.ignoredOverride', { value: globalValues?.[field.key] ?? field.default }) }}</span>
              <button v-if="ignoredOverrides?.includes(field.key) && modelValue[field.key] != null" type="button" class="ml-2 text-primary-600 hover:underline" :disabled="disabled" data-restore-inheritance @click="updateField(field, null)">
                {{ t('admin.customization.alignment.restoreInheritance') }}
              </button>
            </p>
          </div>
          <Select
            :id="id + field.key" class="w-full sm:w-72 sm:shrink-0 lg:w-80"
            :model-value="accountLocked(field) ? null : modelValue[field.key] ?? (mode === 'global' ? field.default : null)" :options="optionsFor(field)"
            :aria-label="fieldText(field, 'label', field.key)" :aria-describedby="id + field.key + '-hint' + (accountLocked(field) ? ' ' + id + field.key + '-locked' : '')"
            :disabled="disabled || accountLocked(field)" @update:model-value="updateField(field, $event)"
          />
        </div>
        <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs">
          <button
            type="button" class="inline-flex items-center gap-1 text-primary-600 hover:underline dark:text-primary-400"
            :aria-expanded="!!expanded[field.key]" :aria-controls="expanded[field.key] ? id + field.key + '-impact' : undefined" @click="expanded[field.key] = !expanded[field.key]"
          >
            <Icon name="chevronDown" size="xs" :class="{ 'rotate-180': expanded[field.key] }" />
            {{ t('admin.customization.alignment.impactToggle') }}
          </button>
          <p v-if="field.hit_stats" class="text-gray-500 dark:text-gray-400">
            {{ t('admin.customization.alignment.hits.label') }}<template v-if="hits?.[field.key]">{{ t('admin.customization.alignment.hits.last24h', { count: hits[field.key].last_24h }) }} / {{ t('admin.customization.alignment.hits.last7d', { count: hits[field.key].last_7d }) }}</template><template v-else>{{ t('admin.customization.alignment.hits.unavailable') }}</template>
          </p>
        </div>
        <ul v-if="expanded[field.key]" :id="id + field.key + '-impact'" class="list-disc space-y-1 break-words pl-5 text-xs leading-5 text-gray-600 dark:text-gray-300">
          <li v-for="(item, index) in impactsFor(field)" :key="index">{{ rt(item) }}</li>
        </ul>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { reactive, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AlignmentHitCounts, ClaudeCustomizationField } from '@/api/admin/settings'
import Select, { type SelectOption } from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'

const props = defineProps<{
  mode: 'global' | 'account'
  accountType?: string
  ignoredOverrides?: string[]
  fields: ClaudeCustomizationField[]
  modelValue: Record<string, string | null>
  hits?: Record<string, AlignmentHitCounts> | null
  globalValues?: Record<string, string>
  disabled?: boolean
}>()
const emit = defineEmits<{ 'update:modelValue': [values: Record<string, string | null>] }>()
const { t, te, tm, rt } = useI18n()
const id = useId()
const expanded = reactive<Record<string, boolean>>({})
const riskClasses = {
  none: 'bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-gray-300',
  low: 'bg-blue-50 text-blue-700 dark:bg-blue-900/40 dark:text-blue-300',
  medium: 'bg-amber-50 text-amber-800 dark:bg-amber-900/40 dark:text-amber-300',
  high: 'bg-red-50 text-red-700 dark:bg-red-900/40 dark:text-red-300',
}

function fieldPath(field: ClaudeCustomizationField) {
  return `admin.customization.alignment.fields.${field.key}`
}

function fieldText(field: ClaudeCustomizationField, key: string, fallback = '') {
  const path = fieldPath(field) + '.' + key
  return te(path) ? t(path) : fallback
}

function impactsFor(field: ClaudeCustomizationField) {
  const path = fieldPath(field) + '.impact'
  return te(path + '.0') ? tm(path) : []
}

function typeLocked(field: ClaudeCustomizationField) {
  return !!(props.accountType && field.account_types?.length && !field.account_types.includes(props.accountType))
}

function accountLocked(field: ClaudeCustomizationField) {
  return props.mode === 'account' && (field.global_only || !field.account_overridable || typeLocked(field) || props.ignoredOverrides?.includes(field.key) === true)
}

function optionsFor(field: ClaudeCustomizationField): SelectOption[] {
  const options: SelectOption[] = (field.options ?? []).map(value => {
    let label = fieldText(field, 'options.' + value, value)
    if (value === field.default) label += t('admin.customization.alignment.defaultSuffix')
    if (value === field.official_value) label += t('admin.customization.alignment.officialSuffix')
    return { value, label }
  })
  if (props.mode === 'account') {
    const globalValue = String(props.globalValues?.[field.key] ?? field.default)
    const value = fieldText(field, 'options.' + globalValue, globalValue)
    options.unshift({ value: null, label: t('admin.customization.alignment.inheritWithValue', { value }) })
  }
  return options
}

function updateField(field: ClaudeCustomizationField, value: SelectOption['value']) {
  if (typeof value === 'string' || value === null) {
    emit('update:modelValue', { ...props.modelValue, [field.key]: value })
  }
}
</script>
