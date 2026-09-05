<template>
  <BaseDialog :show="show" :title="t('admin.workspaces.settlement.title')" @close="handleClose">
    <div class="space-y-4">
      <p class="text-sm text-gray-600 dark:text-gray-400">
        {{ t('admin.workspaces.settlement.intro') }}
      </p>

      <!-- 站长未设上限 = 不开放自设倍率。置灰并说明，免得填完才吃报错 -->
      <p v-if="!canAdjustSettlementRate" class="text-sm text-amber-600 dark:text-amber-400">
        {{ t('admin.workspaces.settlement.notAdjustable') }}
      </p>

      <div v-else class="space-y-2">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <span class="text-sm font-medium text-gray-900 dark:text-white">
            {{ accountLabel }}
          </span>
          <span class="text-xs text-gray-500 dark:text-gray-400">
            {{ rangeHint }}
          </span>
        </div>

        <div class="flex items-center gap-2">
          <input
            v-model.number="draft"
            type="number"
            min="0"
            :max="settlementRateMax ?? undefined"
            step="0.001"
            class="input flex-1"
          />
          <button
            type="button"
            class="btn btn-primary whitespace-nowrap"
            :disabled="saving"
            @click="submit"
          >
            {{ saving ? t('common.saving') : t('common.save') }}
          </button>
        </div>
      </div>
    </div>

    <template #footer>
      <button type="button" class="btn btn-secondary" @click="handleClose">
        {{ t('common.close') }}
      </button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
/**
 * 供应商自设账号结算倍率。
 *
 * 改的就是账号自身的 rate_multiplier —— 计费按账号取该值作为成本口径，
 * 所以这里一个账号一个价，不按分组拆。曾经按 (工作区 × 分组) 定价，那个
 * 口径已废弃：一个账号可同时属于多个分组，落到哪个分组的价上取决于本次
 * 请求命中哪个组，同一个账号的成本因此不唯一，对不上账。
 *
 * 可调区间挂在工作区上（settlement_rate_min/max），对该工作区名下所有
 * 账号统一生效，由后端 applyAccountFieldScope 校验。前端这层只为省来回。
 */
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import { accountsAPI } from '@/api/admin/accounts'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { useMyWorkspaceGrants } from '@/composables/useMyWorkspaceGrants'
import { useAppStore } from '@/stores/app'
import type { Account } from '@/types'

const props = defineProps<{
  show: boolean
  /** 目标账号；null 时对话框不该被打开。 */
  account: Account | null
}>()

// updated 带上后端返回的账号：列表按它原地打补丁，
// 不带就得整表重拉，改一个倍率闪一次全表。
const emit = defineEmits<{ close: []; updated: [account: Account] }>()

const { t } = useI18n()
const appStore = useAppStore()
const {
  settlementRateMin,
  settlementRateMax,
  canAdjustSettlementRate,
  validateSettlementRate,
  load
} = useMyWorkspaceGrants()

const draft = ref<number | null>(null)
const saving = ref(false)

const accountLabel = computed(() => props.account?.name || `#${props.account?.id ?? ''}`)

/** 区间提示：只设上限时不提下限，免得显示成 0 让人以为下限是硬约束。 */
const rangeHint = computed(() => {
  if (settlementRateMax.value == null) return ''
  if (settlementRateMin.value == null) {
    return t('admin.workspaces.settlement.maxHint', { max: settlementRateMax.value })
  }
  return t('admin.workspaces.settlement.rangeHint', {
    min: settlementRateMin.value,
    max: settlementRateMax.value
  })
})

function handleClose(): void {
  emit('close')
}

/**
 * 打开时拉工作区区间并重置草稿。
 *
 * 每次打开都重置：上次改了没提交就关掉的残留值若还留在框里，
 * 会让人误认为那是当前生效的结算价。
 */
watch(
  () => props.show,
  async (open) => {
    if (!open) return
    draft.value = null
    try {
      // 强制刷新而非用缓存：站长可能刚调窄了区间，
      // 拿旧上限填出来的值会在提交时被后端打回。
      await load(true)
    } catch {
      appStore.showError(t('admin.workspaces.settlement.loadFailed'))
      return
    }
    draft.value = props.account?.rate_multiplier ?? null
  },
  { immediate: true }
)

async function submit(): Promise<void> {
  const account = props.account
  const value = draft.value
  if (!account || value == null) return

  const problem = validateSettlementRate(value)
  if (problem === 'closed') {
    appStore.showError(t('admin.workspaces.settlement.notAdjustable'))
    return
  }
  if (problem === 'negative') {
    appStore.showError(t('admin.workspaces.settlement.negative'))
    return
  }
  if (problem === 'above') {
    appStore.showError(
      t('admin.workspaces.settlement.exceedsMax', { max: settlementRateMax.value })
    )
    return
  }
  if (problem === 'below') {
    appStore.showError(
      t('admin.workspaces.settlement.belowMin', { min: settlementRateMin.value })
    )
    return
  }

  saving.value = true
  try {
    const updated = await accountsAPI.update(account.id, { rate_multiplier: value })
    appStore.showSuccess(t('admin.workspaces.settlement.saved'))
    emit('updated', updated)
    emit('close')
  } catch {
    // 具体原因（区间被调窄、账号已转出工作区）由后端错误码经拦截器提示，此处不复述。
  } finally {
    saving.value = false
  }
}
</script>
