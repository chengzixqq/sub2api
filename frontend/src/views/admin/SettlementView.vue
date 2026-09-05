<template>
  <div class="space-y-6">
    <div>
      <h1 class="text-2xl font-semibold text-gray-900 dark:text-white">
        {{ t('admin.settlement.title') }}
      </h1>
      <p class="mt-1 text-sm text-gray-600 dark:text-gray-400">
        {{ t('admin.settlement.subtitle') }}
      </p>
    </div>

    <div v-if="loading" class="card p-6 text-sm text-gray-500 dark:text-gray-400">
      {{ t('common.loading') }}
    </div>

    <!-- 站长误入（手输 URL）：/me 对站长返回空载荷，说明而非留白 -->
    <div v-else-if="!workspace" class="card p-6 text-sm text-gray-500 dark:text-gray-400">
      {{ t('admin.settlement.ownerNotApplicable') }}
    </div>

    <template v-else>
      <div class="card space-y-4 p-4">
        <div>
          <div class="text-sm text-gray-500 dark:text-gray-400">
            {{ t('admin.settlement.workspaceLabel') }}
          </div>
          <div class="mt-1 text-lg font-medium text-gray-900 dark:text-white">
            {{ workspace.name }}
          </div>
        </div>

        <div class="border-t border-gray-200 pt-4 dark:border-dark-600">
          <div class="text-sm text-gray-500 dark:text-gray-400">
            {{ t('admin.settlement.rangeLabel') }}
          </div>
          <div class="mt-1 text-sm text-gray-900 dark:text-white">
            <template v-if="canAdjustSettlementRate">
              {{
                t('admin.settlement.rangeValue', {
                  min: settlementRateMin ?? 0,
                  max: settlementRateMax
                })
              }}
            </template>
            <span v-else class="text-gray-500 dark:text-gray-400">
              {{ t('admin.settlement.noCeiling') }}
            </span>
          </div>
          <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">
            {{ t('admin.settlement.rangeHint') }}
          </p>
        </div>
      </div>

      <p v-if="grants.length === 0" class="card p-6 text-sm text-gray-500 dark:text-gray-400">
        {{ t('admin.settlement.noGrants') }}
      </p>

      <div v-else class="card overflow-hidden">
        <table class="min-w-full divide-y divide-gray-200 dark:divide-dark-600">
          <thead class="bg-gray-50 dark:bg-dark-700/50">
            <tr>
              <th
                class="px-4 py-3 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400"
              >
                {{ t('admin.settlement.columns.group') }}
              </th>
              <th
                class="px-4 py-3 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400"
              >
                {{ t('admin.settlement.columns.priority') }}
              </th>
              <th
                class="px-4 py-3 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400"
              >
                {{ t('admin.settlement.columns.status') }}
              </th>
            </tr>
          </thead>
          <tbody class="divide-y divide-gray-200 dark:divide-dark-600">
            <tr v-for="grant in grants" :key="grant.group_id">
              <td class="px-4 py-3 text-sm text-gray-900 dark:text-white">
                {{ groupLabel(grant.group_id) }}
              </td>
              <td class="px-4 py-3 text-sm text-gray-600 dark:text-gray-400">
                {{ grant.base_priority }}
              </td>
              <td class="px-4 py-3 text-sm">
                <span
                  v-if="grant.enabled"
                  class="rounded bg-green-100 px-1.5 py-0.5 text-xs text-green-700 dark:bg-green-900/40 dark:text-green-300"
                >
                  {{ t('admin.settlement.enabled') }}
                </span>
                <span
                  v-else
                  class="rounded bg-gray-100 px-1.5 py-0.5 text-xs text-gray-600 dark:bg-dark-700 dark:text-gray-300"
                >
                  {{ t('admin.settlement.disabled') }}
                </span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
/**
 * 供应商的结算总览。
 *
 * 只读页：结算倍率挂在账号上，改倍率的入口在账号管理（SettlementRateDialog）。
 * 这里回答的是另一个问题 —— 「我拿到了哪些分组、可调区间是多少」，
 * 即调价前需要先知道的边界，以及对账时需要的授权全貌。
 *
 * 区间挂在工作区而非分组：一个账号可同时属于多个分组，若区间按分组设，
 * 同一账号会摊上多个上限，无从校验。
 *
 * 只显示成本口径，不显示官方原价与用户实付 —— 站长的毛利不属于供应商可见
 * 范围（后端序列化层已剔除那两档金额）。
 */
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import { groupsAPI } from '@/api/admin/groups'
import { useMyWorkspaceGrants } from '@/composables/useMyWorkspaceGrants'
import { useAppStore } from '@/stores/app'

const { t } = useI18n()
const appStore = useAppStore()
const {
  workspace,
  grants,
  loading,
  settlementRateMin,
  settlementRateMax,
  canAdjustSettlementRate,
  load
} = useMyWorkspaceGrants()

/** 分组 ID → 名称。授权行只带 group_id，名称要另外取。 */
const groupNames = ref<Record<number, string>>({})

function groupLabel(groupID: number): string {
  return groupNames.value[groupID] || `#${groupID}`
}

onMounted(async () => {
  // force=true：站长可能刚调窄了区间，本页展示的就是那个边界，不能吃缓存。
  try {
    await load(true)
  } catch {
    appStore.showError(t('admin.settlement.loadFailed'))
    return
  }
  // 分组名单独取：拿不到就退化成 #id 显示，不因此让整页失败。
  // 用 getAll 而非分页 list：授权分组数量有限，且这里只要 id→name 映射。
  try {
    const list = await groupsAPI.getAll()
    const names: Record<number, string> = {}
    for (const g of list) names[g.id] = g.name
    groupNames.value = names
  } catch {
    // 忽略：groupLabel 已有 #id 兜底
  }
})
</script>
