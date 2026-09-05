/**
 * 工作区权限档的前端判据。
 *
 * 这里只做「观感」——把 vendor 点了必然 403 的入口藏掉，或把它改不了的字段
 * 置灰。真正的拦截全在后端：路由白名单默认拒绝、中间件解析作用域、service
 * 层收窄数据。前端漏藏一个按钮只是体验瑕疵，不构成越权。
 *
 * 判据必须与后端 `vendor_routes.go` 的白名单同口径，否则会出现两种坏情况：
 * 前端藏多了让 vendor 用不了已授权的能力，藏少了让人点出 403。
 */

import { computed } from 'vue'

import { useAuthStore } from '@/stores/auth'

export function useWorkspacePerms() {
  const authStore = useAuthStore()

  /** 站长：不受任何限制，所有判据恒为 true。 */
  const isOwner = computed(() => authStore.isOwner)
  const isVendor = computed(() => authStore.isVendor)

  /**
   * 权限档。vendor 才有；旧版后端可能不返回。
   *
   * 未知档位（undefined）与全关是两回事，但对前端显隐是同一结论：
   * 都按「没有这一档」处理，把入口藏掉而非放出去让人吃 403。
   */
  const perms = computed(() => authStore.workspace?.permissions)

  /** 构造一个判据：站长恒真，vendor 看档位，其余身份为假。 */
  function granted(pick: (p: NonNullable<typeof perms.value>) => boolean) {
    return computed(() => {
      if (isOwner.value) return true
      const p = perms.value
      return p ? pick(p) : false
    })
  }

  const canManageAccounts = granted((p) => p.account_manage)
  const canManageProxies = granted((p) => p.proxy_manage)
  const canOpsGroups = granted((p) => p.group_ops)
  const canBillGroups = granted((p) => p.group_billing)
  const canViewMonitor = granted((p) => p.monitor_view)

  /**
   * 分组的新建与删除是站长专属 —— 后端白名单只放行了 GET/PUT/PATCH。
   *
   * 单独列出来而不并入 canOpsGroups：运营档能改已授权分组的调度参数，
   * 但仍然不能建新分组，二者不是同一件事。
   */
  const canCreateGroups = computed(() => isOwner.value)
  const canDeleteGroups = computed(() => isOwner.value)

  /**
   * 调度优先级由站长掌握。
   *
   * vendor 绑定分组时 priority 强制取授权的 base_priority，请求体里传什么
   * 都会被后端忽略，因此输入框直接藏掉，免得填了不生效反而像是 BUG。
   */
  const canSetPriority = computed(() => isOwner.value)

  /**
   * 账号级成本倍率对 vendor 无意义。
   *
   * 计费取值优先级是「工作区授权倍率 → 账号自身倍率 → 1.0」，vendor 的
   * 每一笔都落在第一档上，改账号倍率不会影响他自己的账单，只会在站长
   * 撤销授权后才浮现 —— 这种延迟生效比藏掉更难解释。
   */
  const canSetAccountRate = computed(() => isOwner.value)

  return {
    isOwner,
    isVendor,
    canManageAccounts,
    canManageProxies,
    canOpsGroups,
    canBillGroups,
    canViewMonitor,
    canCreateGroups,
    canDeleteGroups,
    canSetPriority,
    canSetAccountRate
  }
}
