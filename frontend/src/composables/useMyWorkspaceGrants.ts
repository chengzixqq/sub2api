/**
 * vendor 自己的工作区与授权状态。
 *
 * 供应商必须看见两类信息：能用哪些分组（grants），以及自设账号结算倍率的
 * 可调区间（workspace.settlement_rate_min/max）。账号管理页与结算页都要读，
 * 所以状态提在 composable 里，而不是各页自己拉一份。
 *
 * 区间挂在工作区上而非分组授权上：结算倍率本身挂在账号上，一个账号可以同时
 * 属于多个分组，若区间按分组设则同一账号会有多个上限，无从校验。
 *
 * 站长不走这里 —— /admin/workspaces/me 对站长返回空载荷（站长没有「自己的
 * 工作区」），因此 load() 对站长是一次无意义请求，调用方需先判身份。
 */

import { computed, ref } from 'vue'

import { adminWorkspacesAPI, type Workspace, type WorkspaceGrant } from '@/api/admin/workspaces'

/**
 * 模块级单例状态。
 *
 * 授权数据在一次会话里几乎不变（站长改授权是低频运营动作），多个组件
 * 各拉一次纯属浪费。共享同一份的代价是要显式失效 —— 站长可能刚调窄了
 * 区间，涉及倍率输入的入口应传 force=true 重新拉取。
 */
const workspace = ref<Workspace | null>(null)
const grants = ref<WorkspaceGrant[]>([])
const loading = ref(false)
const loaded = ref(false)
/** 并发去重：多个组件同时挂载时只发一次请求。 */
let inflight: Promise<void> | null = null

export function useMyWorkspaceGrants() {
  /** 按 group_id 索引，供账号行判断所属分组是否已授权。 */
  const grantByGroupID = computed(() => {
    const map = new Map<number, WorkspaceGrant>()
    for (const g of grants.value) {
      map.set(g.group_id, g)
    }
    return map
  })

  /**
   * 结算倍率可调区间，null 表示该端不设限。
   *
   * 与 0 不同：null 是站长没设这一端，0 是站长把这一端设成了 0。
   */
  const settlementRateMin = computed<number | null>(
    () => workspace.value?.settlement_rate_min ?? null
  )
  const settlementRateMax = computed<number | null>(
    () => workspace.value?.settlement_rate_max ?? null
  )

  /**
   * 是否允许自设结算倍率，与后端 Scope.CanAdjustSettlementRate 同口径：
   * 站长设了上限才算开放。只设下限不构成开放 —— 没有上限等于不限价。
   */
  const canAdjustSettlementRate = computed(() => settlementRateMax.value != null)

  /**
   * 前端预校验，与后端 Scope.ValidateSettlementRate 同口径。
   *
   * 只为省一个来回，真正的校验在后端 applyAccountFieldScope —— 前端藏掉
   * 输入框也挡不住手工构造的请求。返回 null 表示通过。
   *
   * 负数单独成一档而不并入 'above'：后端两者同为 OutOfRange 一个错误，
   * 但这里要出提示语，把 -1 说成「不得高于上限」会让人对着上限发愣。
   */
  function validateSettlementRate(
    rate: number
  ): 'closed' | 'negative' | 'below' | 'above' | null {
    if (!canAdjustSettlementRate.value) return 'closed'
    if (rate < 0) return 'negative'
    if (rate > settlementRateMax.value!) return 'above'
    if (settlementRateMin.value != null && rate < settlementRateMin.value) return 'below'
    return null
  }

  async function load(force = false): Promise<void> {
    if (loaded.value && !force) return
    if (inflight) return inflight

    loading.value = true
    inflight = (async () => {
      try {
        const data = await adminWorkspacesAPI.getMine()
        workspace.value = data.workspace
        grants.value = data.grants ?? []
        loaded.value = true
      } finally {
        loading.value = false
        inflight = null
      }
    })()
    return inflight
  }

  function reset(): void {
    workspace.value = null
    grants.value = []
    loaded.value = false
  }

  return {
    workspace,
    grants,
    grantByGroupID,
    loading,
    loaded,
    settlementRateMin,
    settlementRateMax,
    canAdjustSettlementRate,
    validateSettlementRate,
    load,
    reset
  }
}
