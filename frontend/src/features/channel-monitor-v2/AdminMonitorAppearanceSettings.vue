<template>
  <section class="space-y-3" data-testid="site-appearance-settings">
    <p v-if="loading" class="text-xs text-gray-500">{{ t('common.loading') }}</p>
    <div v-else-if="!config" role="alert" class="flex items-center gap-3 text-xs text-amber-700"><span>{{ text('显示默认值加载失败。', 'Could not load display defaults.') }}</span><button type="button" class="btn btn-secondary btn-sm" @click="load">{{ t('common.retry') }}</button></div>
    <template v-else>
      <MonitorAppearanceSettings site-defaults :range="range" :bucket-seconds="bucketSeconds" :disabled="saving">
        <div class="mt-4 flex flex-wrap items-center gap-3 border-t border-gray-100 pt-4 text-xs dark:border-dark-700">
          <button type="button" class="btn btn-primary btn-sm" data-testid="save-site-appearance" :disabled="saving || conflict" @click="save">{{ text('保存全站显示默认值', 'Save site display defaults') }}</button>
          <span v-if="saved" role="status">{{ text('全站默认值已保存，个人自定义字段不受影响。', 'Site defaults saved. Personal field overrides are preserved.') }}</span>
        </div>
        <div v-if="conflict" role="alert" class="space-y-2 text-xs text-amber-700"><p>{{ text('服务器配置已改变。草稿已保留，请记录修改后重新载入再合并。', 'Server settings changed. Your draft is retained; record your edits before reloading and merging.') }}</p><button type="button" class="btn btn-secondary btn-sm" @click="load">{{ text('放弃草稿，载入最新配置', 'Discard draft and reload') }}</button></div>
        <p v-else-if="failed" role="alert" class="text-xs text-red-600">{{ text('保存失败，草稿已保留，请重试。', 'Save failed. Your draft is retained; retry saving.') }}</p>
      </MonitorAppearanceSettings>
    </template>
  </section>
</template>
<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getObservationConfig, updateObservationConfig, type MonitorRange, type ObservationConfig } from '@/api/channelMonitorV2'
import MonitorAppearanceSettings from './MonitorAppearanceSettings.vue'
import { normalizeMonitorPreferences, provideMonitorPreferences, useMonitorPreferences } from './useMonitorPreferences'
defineProps<{ range?: MonitorRange; bucketSeconds?: number }>()
const { t, locale } = useI18n()
const text = (zh: string, en: string) => locale.value.startsWith('zh') ? zh : en
const inherited = useMonitorPreferences()
const preferences = provideMonitorPreferences(ref('site-presentation-draft'), { isolated: true, persist: false })
const config = ref<ObservationConfig | null>(null)
const loading = ref(false)
const saving = ref(false)
const conflict = ref(false)
const failed = ref(false)
const saved = ref(false)
watch(preferences, () => { saved.value = false }, { deep: true, flush: 'sync' })
let epoch = 0
onBeforeUnmount(() => { epoch++ })
async function load() {
  if (saving.value) return
  const request = ++epoch
  loading.value = true
  try {
    const result = await getObservationConfig()
    if (request !== epoch) return
    config.value = result
    preferences.setDefaults(result.presentation_defaults, result.version)
    preferences.reset()
    inherited.setDefaults(result.presentation_defaults, result.version)
    conflict.value = false
    failed.value = false
    saved.value = false
  } catch { if (request === epoch) config.value = null }
  finally { if (request === epoch) loading.value = false }
}
async function save() {
  if (!config.value || saving.value || conflict.value) return
  const request = epoch
  saving.value = true
  failed.value = false
  saved.value = false
  try {
    const result = await updateObservationConfig({ ...config.value, presentation_defaults: normalizeMonitorPreferences(preferences.value) })
    if (request !== epoch) return
    config.value = result
    preferences.setDefaults(result.presentation_defaults, result.version)
    preferences.reset()
    inherited.setDefaults(result.presentation_defaults, result.version)
    saved.value = true
  } catch (error) {
    if (request !== epoch) return
    failed.value = true
    conflict.value = (error as { response?: { status?: number } }).response?.status === 409
  } finally { if (request === epoch) saving.value = false }
}
onMounted(load)
</script>
