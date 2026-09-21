<template>
  <div class="relative" ref="containerRef">
    <button
      ref="triggerRef"
      type="button"
      @click="toggle"
      :class="['date-picker-trigger', isOpen && 'date-picker-trigger-open']"
      :aria-expanded="isOpen"
    >
      <span class="date-picker-icon">
        <Icon name="calendar" size="sm" />
      </span>
      <span class="date-picker-value">
        {{ displayValue }}
      </span>
      <span class="date-picker-chevron">
        <Icon
          name="chevronDown"
          size="sm"
          :class="['transition-transform duration-200', isOpen && 'rotate-180']"
        />
      </span>
    </button>

    <Transition name="date-picker-dropdown">
      <div v-if="isOpen" class="date-picker-dropdown" :class="includeTime && 'date-picker-dropdown-time'" :style="includeTime ? popupStyle : undefined">
        <!-- Quick presets -->
        <div class="date-picker-presets">
          <button
            v-for="preset in presets"
            :key="preset.value"
            type="button"
            @click="selectPreset(preset)"
            :class="['date-picker-preset', isPresetActive(preset) && 'date-picker-preset-active']"
          >
            {{ t(preset.labelKey) }}
          </button>
        </div>

        <div class="date-picker-divider"></div>

        <!-- Custom date range inputs -->
        <div class="date-picker-custom" :class="includeTime && 'date-picker-custom-time'">
          <div class="date-picker-field">
            <label class="date-picker-label">{{ startLabel || t('dates.startDate') }}</label>
            <input
              :type="includeTime ? 'datetime-local' : 'date'"
              v-model="localStartDate"
              :max="localEndDate || maximumDate"
              :aria-label="startLabel || t('dates.startDate')"
              class="date-picker-input"
              @change="onDateChange"
            />
          </div>
          <div v-if="!includeTime" class="date-picker-separator">
            <Icon name="arrowRight" size="sm" class="text-gray-400" />
          </div>
          <div class="date-picker-field">
            <label class="date-picker-label">{{ endLabel || t('dates.endDate') }}</label>
            <input
              :type="includeTime ? 'datetime-local' : 'date'"
              v-model="localEndDate"
              :min="localStartDate"
              :max="maximumDate"
              :aria-label="endLabel || t('dates.endDate')"
              class="date-picker-input"
              @change="onDateChange"
            />
          </div>
        </div>

        <!-- Apply button -->
        <div class="date-picker-actions">
          <button v-if="clearable" type="button" class="date-picker-clear" @click="clearRange">
            {{ t('common.clear') }}
          </button>
          <button type="button" @click="apply" class="date-picker-apply" :disabled="!validRange">
            {{ t('dates.apply') }}
          </button>
        </div>
      </div>
    </Transition>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { getFloatingPanelPosition } from '@/utils/floatingPanel'

interface DatePreset {
  labelKey: string
  value: string
  getRange: () => { start: string; end: string }
}

interface Props {
  startDate: string
  endDate: string
  includeTime?: boolean
  requiredRange?: boolean
  clearable?: boolean
  placeholder?: string
  startLabel?: string
  endLabel?: string
}

interface Emits {
  (e: 'update:startDate', value: string): void
  (e: 'update:endDate', value: string): void
  (e: 'change', range: { startDate: string; endDate: string; preset: string | null }): void
}

const props = defineProps<Props>()
const emit = defineEmits<Emits>()

const { t, locale } = useI18n()

const isOpen = ref(false)
const containerRef = ref<HTMLElement | null>(null)
const triggerRef = ref<HTMLButtonElement | null>(null)
const popupStyle = ref<Record<string, string>>({})
const localStartDate = ref(localInputValue(props.startDate))
const localEndDate = ref(localInputValue(props.endDate))
const activePreset = ref<string | null>(props.includeTime ? null : 'last24Hours')

const today = computed(() => {
  // Use local timezone to avoid UTC timezone issues
  const now = new Date()
  const year = now.getFullYear()
  const month = String(now.getMonth() + 1).padStart(2, '0')
  const day = String(now.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
})

// Tomorrow's date - used for max date to handle timezone differences
// When user is in a timezone behind the server, "today" on server might be "tomorrow" locally
const tomorrow = computed(() => {
  const d = new Date()
  d.setDate(d.getDate() + 1)
  return formatDateToString(d)
})

const maximumDate = computed(() => props.includeTime ? `${tomorrow.value}T23:59` : tomorrow.value)

const validRange = computed(() => {
  if (!props.includeTime) return true
  if (props.requiredRange && (!localStartDate.value || !localEndDate.value)) return false
  const start = localStartDate.value ? new Date(resolvedInputValue(localStartDate.value, props.startDate)).getTime() : null
  const end = localEndDate.value ? new Date(resolvedInputValue(localEndDate.value, props.endDate)).getTime() : null
  return (start === null || Number.isFinite(start)) &&
    (end === null || Number.isFinite(end)) &&
    (start === null || end === null || start < end)
})

// Helper function to format date to YYYY-MM-DD using local timezone
function formatDateToString(date: Date): string {
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

function formatDateTimeToString(date: Date): string {
  const hour = String(date.getHours()).padStart(2, '0')
  const minute = String(date.getMinutes()).padStart(2, '0')
  return `${formatDateToString(date)}T${hour}:${minute}`
}

function localInputValue(value: string): string {
  if (!props.includeTime || !/(Z|[+-]\d{2}:\d{2})$/.test(value)) return value
  const date = new Date(value)
  return Number.isFinite(date.getTime()) ? formatDateTimeToString(date) : value
}

function resolvedInputValue(value: string, original: string): string {
  return props.includeTime && value === localInputValue(original) ? original : value
}

const presets: DatePreset[] = [
  {
    labelKey: 'dates.today',
    value: 'today',
    getRange: () => {
      const t = today.value
      return { start: t, end: t }
    }
  },
  {
    labelKey: 'dates.yesterday',
    value: 'yesterday',
    getRange: () => {
      const d = new Date()
      d.setDate(d.getDate() - 1)
      const yesterday = formatDateToString(d)
      return { start: yesterday, end: yesterday }
    }
  },
  {
    labelKey: 'dates.last24Hours',
    value: 'last24Hours',
    getRange: () => {
      const end = new Date()
      const start = new Date(end.getTime() - 24 * 60 * 60 * 1000)
      return {
        start: formatDateToString(start),
        end: formatDateToString(end)
      }
    }
  },
  {
    labelKey: 'dates.last7Days',
    value: '7days',
    getRange: () => {
      const end = today.value
      const d = new Date()
      d.setDate(d.getDate() - 6)
      const start = formatDateToString(d)
      return { start, end }
    }
  },
  {
    labelKey: 'dates.last14Days',
    value: '14days',
    getRange: () => {
      const end = today.value
      const d = new Date()
      d.setDate(d.getDate() - 13)
      const start = formatDateToString(d)
      return { start, end }
    }
  },
  {
    labelKey: 'dates.last30Days',
    value: '30days',
    getRange: () => {
      const end = today.value
      const d = new Date()
      d.setDate(d.getDate() - 29)
      const start = formatDateToString(d)
      return { start, end }
    }
  },
  {
    labelKey: 'dates.thisMonth',
    value: 'thisMonth',
    getRange: () => {
      const now = new Date()
      const start = formatDateToString(new Date(now.getFullYear(), now.getMonth(), 1))
      return { start, end: today.value }
    }
  },
  {
    labelKey: 'dates.lastMonth',
    value: 'lastMonth',
    getRange: () => {
      const now = new Date()
      const start = formatDateToString(new Date(now.getFullYear(), now.getMonth() - 1, 1))
      const end = formatDateToString(new Date(now.getFullYear(), now.getMonth(), 0))
      return { start, end }
    }
  }
]

const getPresetRange = (preset: DatePreset) => {
  const range = preset.getRange()
  if (!props.includeTime) return range
  if (preset.value === 'last24Hours') {
    const end = new Date()
    return {
      start: formatDateTimeToString(new Date(end.getTime() - 24 * 60 * 60 * 1000)),
      end: formatDateTimeToString(end)
    }
  }
  // Datetime consumers use an exclusive end, so calendar presets include the entire last day.
  const end = new Date(`${range.end}T00:00:00`)
  end.setDate(end.getDate() + 1)
  return { start: `${range.start}T00:00`, end: formatDateTimeToString(end) }
}

const displayValue = computed(() => {
  if (activePreset.value) {
    const preset = presets.find((p) => p.value === activePreset.value)
    if (preset) return t(preset.labelKey)
  }

  if (localStartDate.value && localEndDate.value) {
    if (localStartDate.value === localEndDate.value) {
      return formatDate(localStartDate.value)
    }
    return `${formatDate(localStartDate.value)} - ${formatDate(localEndDate.value)}`
  }

  if (props.includeTime && localStartDate.value) {
    return `${props.startLabel || t('dates.startDate')}: ${formatDate(localStartDate.value)}`
  }
  if (props.includeTime && localEndDate.value) {
    return `${props.endLabel || t('dates.endDate')}: ${formatDate(localEndDate.value)}`
  }

  return props.placeholder || t('dates.selectDateRange')
})

const formatDate = (dateStr: string): string => {
  const date = new Date(props.includeTime ? dateStr : dateStr + 'T00:00:00')
  const dateLocale = locale.value === 'zh' ? 'zh-CN' : 'en-US'
  if (props.includeTime) {
    return date.toLocaleString(dateLocale, { year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false })
  }
  return date.toLocaleDateString(dateLocale, { month: 'short', day: 'numeric' })
}

const isPresetActive = (preset: DatePreset): boolean => {
  return activePreset.value === preset.value
}

const selectPreset = (preset: DatePreset) => {
  const range = getPresetRange(preset)
  localStartDate.value = range.start
  localEndDate.value = range.end
  activePreset.value = preset.value
}

const onDateChange = () => {
  // Check if current dates match any preset
  activePreset.value = null
  for (const preset of presets) {
    const range = getPresetRange(preset)
    if (range.start === localStartDate.value && range.end === localEndDate.value) {
      activePreset.value = preset.value
      break
    }
  }
}

const toggle = () => {
  if (props.includeTime) resetDraft()
  isOpen.value = !isOpen.value
  updatePopupPosition()
}

const updatePopupPosition = () => {
  if (!props.includeTime || !isOpen.value || !triggerRef.value) return
  const rect = triggerRef.value.getBoundingClientRect()
  const position = getFloatingPanelPosition(rect, window.innerWidth, window.innerHeight, {
    minComfortableHeight: 340,
    maxHeightRatio: 0.8,
  })
  // Keep the date picker left-aligned while sharing the menu's vertical fit rules.
  const left = Math.max(16, Math.min(rect.left, window.innerWidth - position.width - 16))
  popupStyle.value = {
    position: 'fixed',
    left: `${left}px`,
    width: `${position.width}px`,
    maxHeight: `${position.maxHeight}px`,
    top: position.top === null ? 'auto' : `${position.top}px`,
    bottom: position.bottom === null ? 'auto' : `${position.bottom}px`,
    marginTop: '0',
    overflowY: 'auto',
  }
}

const resetDraft = () => {
  localStartDate.value = localInputValue(props.startDate)
  localEndDate.value = localInputValue(props.endDate)
  onDateChange()
}

const clearRange = () => {
  localStartDate.value = ''
  localEndDate.value = ''
  activePreset.value = null
}

const apply = () => {
  if (!validRange.value) return
  let start = resolvedInputValue(localStartDate.value, props.startDate)
  let end = resolvedInputValue(localEndDate.value, props.endDate)
  if (props.includeTime && props.requiredRange && activePreset.value === 'last24Hours') {
    const endTime = Math.floor(Date.now() / 60_000) * 60_000
    start = new Date(endTime - 86_400_000).toISOString()
    end = new Date(endTime).toISOString()
  }
  emit('update:startDate', start)
  emit('update:endDate', end)
  emit('change', {
    startDate: start,
    endDate: end,
    preset: activePreset.value
  })
  isOpen.value = false
}

const handleClickOutside = (event: MouseEvent) => {
  if (containerRef.value && !containerRef.value.contains(event.target as Node)) {
    if (props.includeTime) resetDraft()
    isOpen.value = false
  }
}

const handleEscape = (event: KeyboardEvent) => {
  if (event.key === 'Escape' && isOpen.value) {
    if (props.includeTime) resetDraft()
    isOpen.value = false
    if (props.includeTime) triggerRef.value?.focus()
  }
}

// Sync local state with props
watch(
  () => props.startDate,
  (val) => {
    localStartDate.value = localInputValue(val)
    onDateChange()
  }
)

watch(
  () => props.endDate,
  (val) => {
    localEndDate.value = localInputValue(val)
    onDateChange()
  }
)

onMounted(() => {
  document.addEventListener('click', handleClickOutside)
  document.addEventListener('keydown', handleEscape)
  if (props.includeTime) {
    window.addEventListener('resize', updatePopupPosition)
    window.addEventListener('scroll', updatePopupPosition, true)
  }
  // Initialize active preset detection
  onDateChange()
})

onUnmounted(() => {
  document.removeEventListener('click', handleClickOutside)
  document.removeEventListener('keydown', handleEscape)
  window.removeEventListener('resize', updatePopupPosition)
  window.removeEventListener('scroll', updatePopupPosition, true)
})
</script>

<style scoped>
.date-picker-trigger {
  @apply flex items-center gap-2;
  @apply rounded-lg px-3 py-2 text-sm;
  @apply bg-white dark:bg-dark-800;
  @apply border border-gray-200 dark:border-dark-600;
  @apply text-gray-700 dark:text-gray-300;
  @apply transition-all duration-200;
  @apply focus:border-primary-500 focus:outline-none focus:ring-2 focus:ring-primary-500/30;
  @apply hover:border-gray-300 dark:hover:border-dark-500;
  @apply cursor-pointer;
}

.date-picker-trigger-open {
  @apply border-primary-500 ring-2 ring-primary-500/30;
}

.date-picker-icon {
  @apply text-gray-400 dark:text-dark-400;
}

.date-picker-value {
  @apply font-medium;
}

.date-picker-chevron {
  @apply text-gray-400 dark:text-dark-400;
}

.date-picker-dropdown {
  @apply absolute left-0 z-[100] mt-2;
  @apply bg-white dark:bg-dark-800;
  @apply rounded-xl;
  @apply border border-gray-200 dark:border-dark-700;
  @apply shadow-lg shadow-black/10 dark:shadow-black/30;
  @apply overflow-hidden;
  @apply min-w-[320px];
}

.date-picker-dropdown-time {
  width: min(320px, calc(100vw - 48px));
  min-width: 0;
}

.date-picker-presets {
  @apply grid grid-cols-2 gap-1 p-2;
}

.date-picker-preset {
  @apply rounded-md px-3 py-1.5 text-xs font-medium;
  @apply text-gray-600 dark:text-gray-400;
  @apply hover:bg-gray-100 dark:hover:bg-dark-700;
  @apply transition-colors duration-150;
}

.date-picker-preset-active {
  @apply bg-primary-100 dark:bg-primary-900/30;
  @apply text-primary-700 dark:text-primary-300;
}

.date-picker-divider {
  @apply border-t border-gray-100 dark:border-dark-700;
}

.date-picker-custom {
  @apply flex items-end gap-2 p-3;
}

.date-picker-custom-time {
  @apply flex-col items-stretch;
}

.date-picker-field {
  @apply flex-1;
}

.date-picker-label {
  @apply mb-1 block text-xs font-medium text-gray-500 dark:text-gray-400;
}

.date-picker-input {
  @apply w-full rounded-md px-2 py-1.5 text-sm;
  @apply bg-gray-50 dark:bg-dark-700;
  @apply border border-gray-200 dark:border-dark-600;
  @apply text-gray-900 dark:text-gray-100;
  @apply focus:border-primary-500 focus:outline-none focus:ring-2 focus:ring-primary-500/30;
}

.date-picker-input::-webkit-calendar-picker-indicator {
  @apply cursor-pointer opacity-60 hover:opacity-100;
  filter: invert(0.5);
}

.dark .date-picker-input::-webkit-calendar-picker-indicator {
  filter: none;
}

.date-picker-separator {
  @apply flex items-center justify-center pb-1;
}

.date-picker-actions {
  @apply flex justify-end gap-2 p-2 pt-0;
}

.date-picker-clear {
  @apply mr-auto rounded-lg px-3 py-1.5 text-sm font-medium;
  @apply text-gray-600 hover:bg-gray-100 dark:text-gray-400 dark:hover:bg-dark-700;
}

.date-picker-apply {
  @apply rounded-lg px-4 py-1.5 text-sm font-medium;
  @apply bg-primary-600 text-white;
  @apply hover:bg-primary-700;
  @apply transition-colors duration-150;
  @apply disabled:cursor-not-allowed disabled:opacity-50;
}

/* Dropdown animation */
.date-picker-dropdown-enter-active,
.date-picker-dropdown-leave-active {
  transition: all 0.2s ease;
}

.date-picker-dropdown-enter-from,
.date-picker-dropdown-leave-to {
  opacity: 0;
  transform: translateY(-8px);
}
</style>
