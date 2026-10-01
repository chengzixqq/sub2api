<template>
  <Teleport to="body">
    <div v-if="show" class="fixed inset-0 z-50 bg-black/30" @click.self="$emit('close')">
      <aside ref="panel" class="absolute inset-y-0 right-0 flex w-full max-w-4xl flex-col bg-white shadow-xl outline-none dark:bg-dark-900" role="dialog" aria-modal="true" :aria-labelledby="titleId" tabindex="-1">
        <header class="flex items-start justify-between gap-3 border-b border-gray-200 p-5 dark:border-dark-700">
          <div><h2 :id="titleId" class="text-base font-semibold text-gray-900 dark:text-white">{{ text('监控设置', 'Monitor settings') }}</h2><p class="mt-1 text-xs text-gray-500">{{ text('集中管理采集、阈值、探测和旧版展示策略。', 'Manage collection, thresholds, probes and legacy display policy.') }}</p></div>
          <button type="button" class="btn btn-secondary btn-icon h-8 w-8" :aria-label="t('common.close')" @click="$emit('close')"><Icon name="x" size="sm" /></button>
        </header>
        <div class="min-h-0 flex-1 overflow-y-auto p-5">
          <nav class="mb-6 flex flex-wrap gap-1 rounded-xl bg-gray-100 p-1 dark:bg-dark-800" :aria-label="text('设置分类', 'Settings sections')">
            <button v-for="item in tabs" :key="item.value" type="button" class="rounded-lg px-3 py-2 text-xs font-medium" :class="tab === item.value ? 'bg-white text-gray-900 shadow-sm dark:bg-dark-700 dark:text-white' : 'text-gray-500 dark:text-gray-400'" :aria-pressed="tab === item.value" @click="tab = item.value">{{ item.label }}</button>
          </nav>
          <ObservationSettingsPanel v-if="tab === 'policy'" />
          <MonitorProbeSettings v-else-if="tab === 'probes'" />
          <MonitorSettingsPanel v-else />
        </div>
      </aside>
    </div>
  </Teleport>
</template>
<script setup lang="ts">
import { computed, getCurrentInstance, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import ObservationSettingsPanel from './ObservationSettingsPanel.vue'
import MonitorProbeSettings from './MonitorProbeSettings.vue'
import MonitorSettingsPanel from './MonitorSettingsPanel.vue'
const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ close: [] }>()
const { t, locale } = useI18n()
const text = (zh: string, en: string) => locale.value.startsWith('zh') ? zh : en
const panel = ref<HTMLElement | null>(null)
const titleId = 'monitor-settings-' + getCurrentInstance()?.uid
const tab = ref<'policy' | 'probes' | 'legacy'>('policy')
const tabs = computed(() => [{ value: 'policy' as const, label: text('监控与阈值', 'Monitor & thresholds') }, { value: 'probes' as const, label: text('探针配置', 'Probe configuration') }, { value: 'legacy' as const, label: text('旧版设置', 'Legacy settings') }])
let previousFocus: HTMLElement | null = null
let previousOverflow = ''
function restore() { document.body.style.overflow = previousOverflow; previousFocus?.focus(); previousFocus = null }
watch(() => props.show, async show => {
  if (show) { tab.value = 'policy'; previousFocus = document.activeElement as HTMLElement | null; previousOverflow = document.body.style.overflow; document.body.style.overflow = 'hidden'; await nextTick(); panel.value?.focus() }
  else if (previousFocus) restore()
}, { immediate: true })
function keydown(event: KeyboardEvent) {
  // Nested editor/confirmation portals keep their own focus and Escape handling.
  if (!props.show || !panel.value?.contains(document.activeElement)) return
  if (event.key === 'Escape') { event.preventDefault(); emit('close'); return }
  if (event.key !== 'Tab') return
  const controls = [...panel.value.querySelectorAll<HTMLElement>('button:not(:disabled),a[href],input:not(:disabled),select:not(:disabled),textarea:not(:disabled),[tabindex="0"]')]
  const first = controls[0]; const last = controls.at(-1)
  if (event.shiftKey && (document.activeElement === first || document.activeElement === panel.value)) { event.preventDefault(); (last || panel.value).focus() }
  else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); (first || panel.value).focus() }
}
onMounted(() => document.addEventListener('keydown', keydown))
onBeforeUnmount(() => { document.removeEventListener('keydown', keydown); if (props.show) restore() })
</script>
