/**
 * 登录后的落地页解析。
 *
 * 三种身份落到三个地方：普通用户去用户面板，站长去管理端总览，
 * vendor 去自己权限档允许的第一个管理页。
 */

import type { CurrentUserWorkspace } from '@/types'

/** 用户面板：普通用户，以及作用域不明的兜底目标。 */
export const USER_LANDING = '/dashboard'

/** 管理端总览：站长专属，页内多个端点对 vendor 是 403。 */
export const OWNER_LANDING = '/admin/dashboard'

/**
 * vendor 落地页的候选顺序。
 *
 * 按「日常最常用」排序而非按权限档定义顺序：供应商开工第一件事是看账号池，
 * 其次才是分组与代理。监控排最后 —— 它是只读档，通常与其他档同时开启，
 * 单独开时才会成为落地页。
 */
const VENDOR_LANDING_CANDIDATES: Array<{
  path: string
  granted: (perms: NonNullable<CurrentUserWorkspace['permissions']>) => boolean
}> = [
  { path: '/admin/accounts', granted: (p) => p.account_manage },
  { path: '/admin/groups', granted: (p) => p.group_ops || p.group_billing },
  { path: '/admin/proxies', granted: (p) => p.proxy_manage },
  { path: '/admin/usage', granted: (p) => p.monitor_view },
  // 账号只读：仅开监控档时账号列表也可看（后端 permitAccountRead 同口径）。
  { path: '/admin/accounts', granted: (p) => p.monitor_view }
]

/**
 * 解析 vendor 的落地页。
 *
 * 权限档全关（站长建了工作区但还没开权限）时返回用户面板：此时管理端
 * 每一页都会 403，把人丢进去只会看到一片报错。
 */
export function resolveVendorLanding(workspace: CurrentUserWorkspace | null): string {
  const perms = workspace?.permissions
  if (!perms) {
    return USER_LANDING
  }
  for (const candidate of VENDOR_LANDING_CANDIDATES) {
    if (candidate.granted(perms)) {
      return candidate.path
    }
  }
  return USER_LANDING
}

/**
 * 解析任意身份登录后应落到的路径。
 *
 * requestedRedirect 优先（用户点深链被拦到登录页的场景），
 * 但仅当它不是登录页自身时才采纳，避免自跳循环。
 */
export function resolveLandingPath(input: {
  isOwner: boolean
  isVendor: boolean
  workspace: CurrentUserWorkspace | null
  requestedRedirect?: string
}): string {
  const requested = input.requestedRedirect?.trim()
  if (requested && requested !== '/login' && !requested.startsWith('/login?')) {
    return requested
  }
  if (input.isOwner) {
    return OWNER_LANDING
  }
  if (input.isVendor) {
    return resolveVendorLanding(input.workspace)
  }
  return USER_LANDING
}
