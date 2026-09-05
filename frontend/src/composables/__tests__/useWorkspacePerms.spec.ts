import { describe, it, expect, beforeEach, vi } from 'vitest'
import { reactive } from 'vue'

// authStore 用 reactive 对象 mock：每个用例改字段即可切换身份，
// 不必为了换一个布尔值去构造真实 pinia store。
//
// 必须是 reactive 而非普通对象 —— 被测判据是 computed，只有响应式源
// 才能触发重算。真实 authStore 是 pinia store，本身即响应式。
const mockAuth = reactive({
  isOwner: false,
  isVendor: false,
  workspace: null as { permissions?: Record<string, boolean> } | null
})

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => mockAuth
}))

import { useWorkspacePerms } from '@/composables/useWorkspacePerms'

/** 权限档全开。用于断言「即便档位全开，站长专属项仍不放行」。 */
function allPerms() {
  return {
    account_manage: true,
    group_ops: true,
    group_billing: true,
    proxy_manage: true,
    monitor_view: true
  }
}

describe('useWorkspacePerms', () => {
  beforeEach(() => {
    mockAuth.isOwner = false
    mockAuth.isVendor = false
    mockAuth.workspace = null
  })

  // 工作区机制的硬约束：站长视角与引入之前逐字一致。
  // 站长没有工作区，perms 恒为 undefined，若判据只看档位就会全部误判为 false，
  // 结果是站长自己丢掉建分组、删分组、改优先级的入口。
  it('站长在没有工作区时所有判据仍为真', () => {
    mockAuth.isOwner = true

    const p = useWorkspacePerms()

    expect(p.canManageAccounts.value).toBe(true)
    expect(p.canOpsGroups.value).toBe(true)
    expect(p.canBillGroups.value).toBe(true)
    expect(p.canManageProxies.value).toBe(true)
    expect(p.canViewMonitor.value).toBe(true)
    expect(p.canCreateGroups.value).toBe(true)
    expect(p.canDeleteGroups.value).toBe(true)
    expect(p.canSetPriority.value).toBe(true)
    expect(p.canSetAccountRate.value).toBe(true)
  })

  it('vendor 按权限档逐项放行', () => {
    mockAuth.isVendor = true
    mockAuth.workspace = {
      permissions: { ...allPerms(), group_billing: false, proxy_manage: false }
    }

    const p = useWorkspacePerms()

    expect(p.canManageAccounts.value).toBe(true)
    expect(p.canOpsGroups.value).toBe(true)
    expect(p.canViewMonitor.value).toBe(true)
    expect(p.canBillGroups.value).toBe(false)
    expect(p.canManageProxies.value).toBe(false)
  })

  // 后端白名单只放行分组的 GET/PUT/PATCH，POST 与 DELETE 压根不在表里。
  // 这几项若跟着 group_ops 走，vendor 会看到按钮、点下去吃 403。
  it('分组新建删除与优先级、账号倍率对 vendor 恒为假', () => {
    mockAuth.isVendor = true
    mockAuth.workspace = { permissions: allPerms() }

    const p = useWorkspacePerms()

    expect(p.canCreateGroups.value).toBe(false)
    expect(p.canDeleteGroups.value).toBe(false)
    expect(p.canSetPriority.value).toBe(false)
    expect(p.canSetAccountRate.value).toBe(false)
  })

  // 旧版后端不返回 permissions。未知档位与全关对显隐是同一结论：
  // 藏掉入口，而不是当作不受限放出去。
  it('权限档缺失时一律不放行', () => {
    mockAuth.isVendor = true
    mockAuth.workspace = {}

    const p = useWorkspacePerms()

    expect(p.canManageAccounts.value).toBe(false)
    expect(p.canOpsGroups.value).toBe(false)
    expect(p.canBillGroups.value).toBe(false)
  })

  // 判据是 computed，身份变化后必须重算。
  // 若写成一次性求值，登录态刷新（/auth/me 返回后 store 才填充）会让
  // 界面停在登录瞬间的旧判据上。
  it('身份变化后判据随之更新', () => {
    const p = useWorkspacePerms()
    expect(p.canCreateGroups.value).toBe(false)

    mockAuth.isOwner = true
    expect(p.canCreateGroups.value).toBe(true)
  })
})
