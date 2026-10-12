<template>
  <section class="mx-auto w-full max-w-6xl space-y-5 px-1 py-2 sm:px-2">
    <header
      class="page-header mb-0 flex flex-wrap items-center justify-between gap-3 rounded-3xl bg-white p-5 shadow-sm ring-1 ring-gray-900/5 dark:bg-dark-800 dark:ring-dark-700 sm:p-6"
    >
      <div class="min-w-0">
        <h2 class="page-title flex items-center gap-2 text-xl font-black text-gray-900 dark:text-white">
          <span class="inline-flex h-8 w-8 items-center justify-center rounded-xl bg-blue-50 text-blue-500 dark:bg-blue-900/30 dark:text-blue-400">
            <Icon name="chart" size="sm" />
          </span>
          {{ text('旧聚合 V2 兼容配置', 'Legacy V2 aggregation compatibility') }}
        </h2>
        <p class="page-description mt-1.5 text-xs text-gray-500 dark:text-gray-400">
          {{ text('管理旧日志聚合维度、调度频率与原V2评分参数；不替代统一卡片的监控策略。', 'Manage legacy log dimensions, scheduling and original V2 scoring; this does not replace the unified card policy.') }}
        </p>
      </div>
      <button
        type="button"
        class="btn btn-primary"
        :disabled="loading || saving || !dirty || conflict"
        @click="save"
      >
        <Icon name="check" size="sm" />
        {{ t('channelMonitorV2.settings.save') }}
      </button>
    </header>

    <div class="space-y-1 rounded-xl border border-gray-200 bg-gray-50 p-3 text-xs leading-5 text-gray-600 dark:border-dark-700 dark:bg-dark-800/40 dark:text-gray-300">
      <p>{{ text('这里管理旧聚合V2的兼容参数。影子模式及尚未切换至实时发布的分组继续读取旧日志聚合；已实时发布的分组使用新采集。', 'These are compatibility settings for the legacy V2 aggregation. Shadow mode and groups not yet published live use legacy logs; live groups use the new collector.') }}</p>
      <p>{{ text('统一卡片的可靠率、最小样本和首字延迟等级始终使用“监控与阈值”。这里的权重、错误率和缓存健康阈值供旧聚合接口与高级视图使用。', 'Unified cards always use the reliability, minimum sample and latency policy in Monitor & thresholds. Weights, error rates and cache health thresholds here serve legacy aggregation endpoints and advanced views.') }}</p>
      <p v-if="!preview && !systemEnabled" class="text-amber-700 dark:text-amber-300">{{ text('系统监控入口当前关闭，旧聚合采集暂停；仍可在此预先配置。', 'System monitoring is disabled, so legacy aggregation is paused; configuration remains editable.') }}</p>
    </div>

    <p v-if="savedMessage" role="status" data-testid="settings-saved" class="rounded-xl border border-emerald-200 bg-emerald-50 px-3 py-2 text-sm text-emerald-800 dark:border-emerald-900 dark:bg-emerald-950/30 dark:text-emerald-200">{{ savedMessage }}</p>
    <p v-if="preview" class="rounded-xl bg-primary-50 p-3 text-xs text-primary-700 dark:bg-primary-950/30 dark:text-primary-300">{{ text('本地验收：旧版设置只保存在内存，不连接后台。', 'Local preview: legacy settings stay in memory, without backend requests.') }}</p>
    <p v-if="validation" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ validation }}</p>
    <div v-if="conflict" role="alert" class="space-y-2 rounded-xl bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-950/30 dark:text-amber-200"><p>{{ text('服务器配置已改变，你的草稿已保留。记录修改后，载入最新版本再合并。', 'Server settings changed; your draft is retained. Record your changes, reload and merge.') }}</p><button type="button" class="btn btn-secondary btn-sm" @click="load">{{ text('放弃草稿，载入最新配置', 'Discard draft and reload') }}</button></div>
    <div
      v-if="loading"
      class="card flex min-h-[200px] items-center justify-center !rounded-3xl !border-0 text-sm text-gray-400 shadow-sm ring-1 ring-gray-900/5 dark:ring-dark-700"
    >
      <span class="animate-pulse">{{ t('channelMonitorV2.settings.loading') }}</span>
    </div>

    <div v-else-if="!draft" role="alert" class="flex items-center gap-3 text-sm text-amber-700"><span>{{ t('channelMonitorV2.settings.loadFailed') }}</span><button type="button" class="btn btn-secondary btn-sm" @click="load">{{ t('common.retry') }}</button></div>
    <fieldset v-else :disabled="saving" class="space-y-5">
      <div class="card divide-y divide-gray-100 !rounded-3xl !border-0 shadow-sm ring-1 ring-gray-900/5 dark:divide-dark-700 dark:!bg-dark-800 dark:ring-dark-700">
        <div class="flex flex-wrap items-center justify-between gap-4 px-5 py-4">
          <div>
            <strong class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('channelMonitorV2.settings.enableTitle') }}</strong>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">
              {{ text('系统监控入口开启时，V1/V2界面模式均可执行旧日志聚合；关闭此项仅停止旧聚合。', 'Legacy aggregation can run in either UI mode while system monitoring is enabled. This switch stops only legacy aggregation.') }}
            </p>
          </div>
          <Toggle v-model="draft.enabled" />
        </div>
        <div class="flex flex-wrap items-center justify-between gap-4 px-5 py-4">
          <div>
            <strong class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('channelMonitorV2.settings.refreshTitle') }}</strong>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">{{ text('旧日志聚合的调度周期；不改变矩阵时间桶宽度，也不是页面自动刷新周期。', 'Scheduling interval for legacy aggregation; it does not change matrix bucket width or page polling.') }}</p>
          </div>
          <div class="tabs inline-flex w-auto" role="group" :aria-label="t('channelMonitorV2.settings.refreshAria')">
            <button
              type="button"
              class="tab"
              :class="draft.refresh_interval_seconds === 60 ? 'tab-active' : ''"
              @click="draft.refresh_interval_seconds = 60"
            >
              1 min
            </button>
            <button
              type="button"
              class="tab"
              :class="draft.refresh_interval_seconds === 300 ? 'tab-active' : ''"
              @click="draft.refresh_interval_seconds = 300"
            >
              5 min
            </button>
          </div>
        </div>
      </div>

      <div class="card overflow-hidden !rounded-3xl !border-0 shadow-sm ring-1 ring-gray-900/5 dark:!bg-dark-800 dark:ring-dark-700">
        <div class="card-header !py-3">
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('channelMonitorV2.settings.platformsTitle') }}</h3>
          <p class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">
            {{ t('channelMonitorV2.settings.platformsHint') }}
          </p>
        </div>
        <div class="divide-y divide-gray-100 dark:divide-dark-700">
          <div
            v-for="platform in draft.platforms"
            :key="platform.platform"
            class="grid grid-cols-1 items-center gap-3 px-5 py-3 sm:grid-cols-[auto_7rem_minmax(0,1fr)_auto]"
          >
            <Toggle v-model="platform.enabled" />
            <strong class="text-sm font-medium text-gray-900 dark:text-white">{{ platformLabel(platform.platform) }}</strong>
            <input
              class="input"
              :value="platform.models.join(', ')"
              type="text"
              :placeholder="t('channelMonitorV2.settings.modelsPlaceholder')"
              @input="setModels(platform, $event)"
            />
            <span
              class="badge justify-self-start sm:justify-self-end"
              :class="platform.models.length ? 'badge-gray' : 'badge badge-primary'"
            >
              {{ platform.models.length ? t('channelMonitorV2.settings.badgeOther') : t('channelMonitorV2.settings.badgeAllModels') }}
            </span>
          </div>
        </div>
      </div>

      <div class="card overflow-hidden !rounded-3xl !border-0 shadow-sm ring-1 ring-gray-900/5 dark:!bg-dark-800 dark:ring-dark-700">
        <div class="card-header flex flex-wrap items-center justify-between gap-2 !py-3">
          <div>
            <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('channelMonitorV2.settings.groupsTitle') }}</h3>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">
              {{
                draft.group_ids.length
                  ? t('channelMonitorV2.settings.groupsSelected', { count: draft.group_ids.length })
                  : t('channelMonitorV2.settings.groupsAll')
              }}
            </p>
          </div>
          <button
            v-if="draft.group_ids.length"
            type="button"
            class="btn btn-ghost btn-sm"
            @click="draft.group_ids = []"
          >
            {{ t('channelMonitorV2.settings.groupsAll') }}
          </button>
        </div>
        <div class="max-h-[min(40vh,280px)] overflow-y-auto px-3 py-2 sm:px-4">
          <div class="grid grid-cols-1 gap-1 sm:grid-cols-2">
            <label
              v-for="group in groups"
              :key="group.id"
              class="flex cursor-pointer items-center gap-3 rounded-xl px-3 py-2.5 text-sm transition hover:bg-gray-50 dark:hover:bg-dark-800/60"
            >
              <input
                type="checkbox"
                class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500/40"
                :checked="draft.group_ids.includes(group.id)"
                @change="toggleGroup(group.id)"
              />
              <span class="min-w-0 flex-1 truncate font-medium text-gray-800 dark:text-gray-100">{{ group.name }}</span>
              <small class="shrink-0 text-xs text-gray-400">{{ platformLabel(group.platform) }} · #{{ group.id }}</small>
            </label>
          </div>
          <p v-if="groups.length === 0" class="empty-state py-8 text-sm text-gray-400">{{ t('channelMonitorV2.settings.groupsEmpty') }}</p>
        </div>
      </div>

      <div class="card overflow-hidden !rounded-3xl !border-0 shadow-sm ring-1 ring-gray-900/5 dark:!bg-dark-800 dark:ring-dark-700">
        <div class="card-header !py-3">
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('channelMonitorV2.settings.errorsTitle') }}</h3>
          <p class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">
            {{ t('channelMonitorV2.settings.errorsHint') }}
          </p>
        </div>
        <div class="max-h-[min(40vh,320px)] overflow-y-auto px-3 py-2 sm:px-4">
          <div class="grid grid-cols-1 gap-1 sm:grid-cols-2">
            <label
              v-for="category in errorCategories"
              :key="category"
              class="flex cursor-pointer items-center gap-3 rounded-xl px-3 py-2.5 text-sm transition hover:bg-gray-50 dark:hover:bg-dark-800/60"
            >
              <input
                type="checkbox"
                class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500/40"
                :checked="isCategoryIgnored(category)"
                @change="toggleIgnoredCategory(category)"
              />
              <span class="min-w-0 flex-1 truncate font-medium text-gray-800 dark:text-gray-100">
                {{ categoryLabel(category) }}
              </span>
              <small class="shrink-0 font-mono text-[10px] text-gray-400">{{ category }}</small>
            </label>
          </div>
        </div>
        <div class="border-t border-gray-100 px-5 py-3 text-xs text-gray-500 dark:border-dark-700 dark:text-dark-400">
          {{
            t('channelMonitorV2.settings.ignoredSummary', {
              ignored: draft.ignored_error_categories?.length || 0,
              counted: countedErrorCategoryCount,
            })
          }}
        </div>
      </div>

      <div class="card overflow-hidden !rounded-3xl !border-0 shadow-sm ring-1 ring-gray-900/5 dark:!bg-dark-800 dark:ring-dark-700">
        <div class="card-header !py-3">
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('channelMonitorV2.settings.healthTitle') }}</h3>
          <p class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">
            {{ text('用于原V2聚合输出的健康分与分级；统一卡片（含旧数据行）使用“监控与阈值”的可靠率与延迟规则。', 'Health scoring for original V2 aggregation. Unified cards, including legacy rows, use Monitor & thresholds reliability and latency rules.') }}
          </p>
        </div>
        <div class="grid grid-cols-1 gap-4 px-5 py-4 sm:grid-cols-2 lg:grid-cols-4">
          <label class="block">
            <span class="input-label">{{ t('channelMonitorV2.settings.fields.minimumSample') }}</span>
            <input v-model.number="draft.health_thresholds.minimum_sample" class="input" type="number" min="1" max="10000" />
          </label>
          <label class="block">
            <span class="input-label">{{ t('channelMonitorV2.settings.fields.warningError') }}</span>
            <input v-model.number="warningErrorPercent" class="input" type="number" min="0.01" max="100" step="0.01" />
          </label>
          <label class="block">
            <span class="input-label">{{ t('channelMonitorV2.settings.fields.criticalError') }}</span>
            <input v-model.number="criticalErrorPercent" class="input" type="number" min="0.01" max="100" step="0.01" />
          </label>
          <label class="block">
            <span class="input-label">{{ t('channelMonitorV2.settings.fields.targetTtft') }}</span>
            <input v-model.number="draft.health_thresholds.target_ttft_ms" class="input" type="number" min="1" step="100" />
          </label>
          <label class="block">
            <span class="input-label">{{ t('channelMonitorV2.settings.fields.warningTtft') }}</span>
            <input v-model.number="draft.health_thresholds.warning_ttft_ms" class="input" type="number" min="1" step="100" />
          </label>
          <label class="block">
            <span class="input-label">{{ t('channelMonitorV2.settings.fields.criticalTtft') }}</span>
            <input v-model.number="draft.health_thresholds.critical_ttft_ms" class="input" type="number" min="1" step="100" />
          </label>
          <label class="block">
            <span class="input-label">{{ t('channelMonitorV2.settings.fields.warningCache') }}</span>
            <input v-model.number="warningCachePercent" class="input" type="number" min="0" max="100" step="0.1" />
          </label>
          <label class="block">
            <span class="input-label">{{ t('channelMonitorV2.settings.fields.criticalCache') }}</span>
            <input v-model.number="criticalCachePercent" class="input" type="number" min="0" max="100" step="0.1" />
          </label>
        </div>
        <div class="border-t border-gray-100 px-5 py-4 dark:border-dark-700">
          <p class="mb-3 text-xs text-gray-500">{{ text('健康评分权重：每项0–100%，合计必须为100%。缓存阈值均为0时，缓存未命中不影响健康。', 'Health weights: each 0–100%, totaling 100%. Zero cache thresholds disable cache-miss health penalties.') }}</p>
          <div class="grid gap-4 sm:grid-cols-3"><label v-for="field in weightFields" :key="field.key"><span class="input-label">{{ field.label }}</span><input :value="percentValue(draft.health_thresholds[field.key])" :data-testid="field.key" class="input" type="number" min="0" max="100" step="0.1" @input="setWeight(field.key, $event)" /></label></div>
        </div>
      </div>

      <div class="space-y-2">
        <div class="rounded-2xl border border-primary-200 bg-primary-50/80 px-4 py-3 text-sm text-primary-900 dark:border-primary-800/50 dark:bg-primary-900/20 dark:text-primary-100">
          <template v-if="namedModelCount === 0">
            {{ t('channelMonitorV2.settings.namedModelsEmpty') }}
          </template>
          <template v-else>
            {{ t('channelMonitorV2.settings.namedModelsCount', { count: namedModelCount }) }}
          </template>
        </div>
        <div class="rounded-2xl border border-gray-200 bg-gray-50/80 px-4 py-3 text-xs text-gray-600 dark:border-dark-600 dark:bg-dark-800/50 dark:text-gray-300">
          <p class="font-medium text-gray-800 dark:text-gray-100">{{ text('旧聚合输出与统一卡片的区别', 'Legacy output versus unified cards') }}</p>
          <ul class="mt-1.5 list-disc space-y-0.5 pl-4">
            <li>{{ text('旧综合评分权重可在上方配置，当前草稿：', 'Legacy score weights are configurable above. Current draft:') }} {{ text('错误率', 'Errors') }} {{ percentValue(draft.health_thresholds.error_weight) }}% · {{ text('首字延迟', 'TTFT') }} {{ percentValue(draft.health_thresholds.ttft_weight) }}% · {{ text('缓存', 'Cache') }} {{ percentValue(draft.health_thresholds.cache_weight) }}%</li>
            <li>{{ text('矩阵、趋势和卡片取自同一授权概览，指标内容依据各视图选择。', 'Matrix, trend and cards share the authorized overview; each view selects the metrics it presents.') }}</li>
            <li>{{ text('原V2延迟接口提供 AVG、P50、P90、P95；统一卡片主值使用P50，可选P95详情。普通用户不显示绝对请求数/错误数。', 'Original V2 latency responses include AVG, P50, P90 and P95. Unified cards use P50 with optional P95 detail; absolute request/error counts remain private.') }}</li>
            <li>{{ t('channelMonitorV2.settings.userContract.models') }}</li>
          </ul>
        </div>
      </div>
    </fieldset>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import Icon from '@/components/icons/Icon.vue'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import {
  getConfig,
  updateConfig,
  MONITOR_ERROR_CATEGORIES,
  type MonitorConfig,
} from '@/api/channelMonitorV2'
import { adminAPI } from '@/api/admin'
import { cloneSettings, monitorSettingsPreview, previewGroups } from './monitorSettingsPreview'
import { finite, validateLegacy } from './monitorSettingsValidation'

const props = withDefaults(defineProps<{ preview?: boolean }>(), { preview: false })
const emit = defineEmits<{ saved: [] }>()
const { t, te, locale } = useI18n()
const text = (zh: string, en: string) => (locale?.value || 'zh').startsWith('zh') ? zh : en
const validation = ref('')
const savedMessage = ref('')
const conflict = ref(false)
const appStore = useAppStore()
const loading = ref(true)
const saving = ref(false)
const draft = ref<MonitorConfig | null>(null)
const original = ref('')
const groups = ref<Array<{ id: number; name: string; platform: string }>>([])

const dirty = computed(() => (draft.value ? JSON.stringify(draft.value) !== original.value : false))
const namedModelCount = computed(
  () => draft.value?.platforms.filter((p) => p.enabled).reduce((sum, p) => sum + p.models.length, 0) || 0
)
const errorCategories = MONITOR_ERROR_CATEGORIES
const countedErrorCategoryCount = computed(
  () => errorCategories.length - (draft.value?.ignored_error_categories?.length || 0)
)
const systemEnabled = computed(() => Boolean(appStore.cachedPublicSettings?.channel_monitor_enabled))
const defaultThresholds = {
  minimum_sample: 50,
  warning_error_rate: 0.05,
  critical_error_rate: 0.20,
  target_ttft_ms: 3000,
  warning_ttft_ms: 3000,
  critical_ttft_ms: 10000,
  // Match server defaults: zero cache thresholds disable cache-miss health penalties.
  warning_cache_rate: 0,
  critical_cache_rate: 0,
  error_weight: 0.60,
  ttft_weight: 0.20,
  cache_weight: 0.20,
}

/** Factory ignored categories (matches backend DefaultChannelMonitorV2IgnoredErrorCategories). */
const defaultIgnoredErrorCategories = [
  'authentication',
  'client_cancelled',
  'content_policy',
  'context_limit',
  'group_access',
  'model_unsupported',
  'not_found',
  'quota_or_balance',
] as const
function percentModel(key: 'warning_error_rate' | 'critical_error_rate' | 'warning_cache_rate' | 'critical_cache_rate') {
  return computed({
    get: () => percentValue(draft.value?.health_thresholds?.[key]),
    set: (value: number | string) => {
      if (!draft.value) return
      draft.value.health_thresholds[key] = value === '' ? Number.NaN : Number(value) / 100
    },
  })
}
function percentValue(value: unknown) { return finite(value) ? value * 100 : '' }
const weightFields = computed(() => [{ key: 'error_weight' as const, label: text('错误率权重（%）', 'Error weight (%)') }, { key: 'ttft_weight' as const, label: text('首字延迟权重（%）', 'TTFT weight (%)') }, { key: 'cache_weight' as const, label: text('缓存权重（%）', 'Cache weight (%)') }])
function setWeight(key: 'error_weight' | 'ttft_weight' | 'cache_weight', event: Event) {
  if (!draft.value) return
  const value = (event.target as HTMLInputElement).value
  draft.value.health_thresholds[key] = value === '' ? Number.NaN : Number(value) / 100
}
const warningErrorPercent = percentModel('warning_error_rate')
const criticalErrorPercent = percentModel('critical_error_rate')
const warningCachePercent = percentModel('warning_cache_rate')
const criticalCachePercent = percentModel('critical_cache_rate')

function setModels(platform: MonitorConfig['platforms'][number], event: Event) {
  platform.models = [
    ...new Set(
      (event.target as HTMLInputElement).value
        .split(',')
        .map((v) => v.trim())
        .filter(Boolean)
    ),
  ].sort()
}

function toggleGroup(id: number) {
  if (!draft.value) return
  draft.value.group_ids = draft.value.group_ids.includes(id)
    ? draft.value.group_ids.filter((value) => value !== id)
    : [...draft.value.group_ids, id].sort((a, b) => a - b)
}

function isCategoryIgnored(category: string): boolean {
  return Boolean(draft.value?.ignored_error_categories?.includes(category))
}

function toggleIgnoredCategory(category: string) {
  if (!draft.value) return
  const current = new Set(draft.value.ignored_error_categories || [])
  if (current.has(category)) current.delete(category)
  else current.add(category)
  draft.value.ignored_error_categories = [...current].sort()
}

function categoryLabel(category: string) {
  const key = `channelMonitorV2.errorCategories.${category}`
  return te(key) ? t(key) : category
}

function platformLabel(value: string) {
  const key = `channelMonitorV2.platforms.${value}`
  if (te(key)) return t(key)
  return value
}

function normalizeConfig(value: MonitorConfig): MonitorConfig {
  const ignored = value.ignored_error_categories
  return {
    ...value,
    health_thresholds: { ...defaultThresholds, ...(value.health_thresholds || {}) },
    // Preserve explicit empty arrays from the server (operator cleared all).
    ignored_error_categories: [
      ...(ignored == null ? [...defaultIgnoredErrorCategories] : ignored),
    ].sort(),
  }
}

let epoch = 0
onBeforeUnmount(() => { epoch++ })
async function load() {
  if (saving.value) return
  const request = ++epoch
  loading.value = true
  try {
    const [value, groupRows] = props.preview ? [monitorSettingsPreview.getLegacy(), previewGroups] : await Promise.all([getConfig(), adminAPI.groups.getAllIncludingInactive()])
    if (request !== epoch) return
    const normalized = normalizeConfig(value)
    draft.value = cloneSettings(normalized)
    groups.value = groupRows
    original.value = JSON.stringify(normalized)
    conflict.value = false
    validation.value = ''
  } catch (error) {
    if (request === epoch) appStore.showError(extractApiErrorMessage(error, t('channelMonitorV2.settings.loadFailed')))
  } finally { if (request === epoch) loading.value = false }
}
async function save() {
  if (!draft.value || saving.value || loading.value || conflict.value) return
  const problem = validateLegacy(draft.value)
  const messages: Record<string, string> = {
    errors: text('错误率必须满足：0 < 警告线 ≤ 严重线 ≤ 100%，不可留空。', 'Error rates must satisfy 0 < warning ≤ critical ≤ 100%; blanks are not allowed.'),
    latency: text('延迟必须为整数，1 ≤ 目标 ≤ 警告线 ≤ 严重线 ≤ 86400000毫秒。', 'Latency must be integer milliseconds: 1 ≤ target ≤ warning ≤ critical ≤ 86400000.'),
    cache: text('缓存阈值必须满足：0 ≤ 严重线 ≤ 警告线 ≤ 100%。', 'Cache thresholds must satisfy 0 ≤ critical ≤ warning ≤ 100%.'),
    weights: text('三项权重均需在0–100%内，合计为100%，不可留空。', 'Each weight must be 0–100%, with a total of 100%; blanks are not allowed.'),
  }
  if (problem) { validation.value = messages[problem] || text('参数为空或超出允许范围。', 'A parameter is blank or outside its allowed range.'); return }
  validation.value = ''
  savedMessage.value = ''
  saving.value = true
  const request = epoch
  try {
    const payload = cloneSettings(normalizeConfig(draft.value))
    const value = props.preview ? monitorSettingsPreview.saveLegacy(payload) : await updateConfig(payload)
    if (request !== epoch) return
    const normalized = normalizeConfig(value)
    draft.value = cloneSettings(normalized)
    original.value = JSON.stringify(normalized)
    emit('saved')
    savedMessage.value = props.preview ? text('上次配置保存成功（本地预览，不影响线上）。', 'Last settings save succeeded locally; production is unchanged.') : t('channelMonitorV2.settings.saveSuccess')
    appStore.showSuccess(props.preview ? text('已保存本地预览配置', 'Local preview settings saved') : t('channelMonitorV2.settings.saveSuccess'))
  } catch (error) {
    if (request !== epoch) return
    appStore.showError(extractApiErrorMessage(error, t('channelMonitorV2.settings.saveFailed')))
    conflict.value = (error as { response?: { status?: number } }).response?.status === 409
  } finally { if (request === epoch) saving.value = false }
}
onMounted(load)
</script>
