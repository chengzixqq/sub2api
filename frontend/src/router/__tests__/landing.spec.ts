import { describe, expect, it } from 'vitest'

import {
  OWNER_LANDING,
  USER_LANDING,
  resolveLandingPath,
  resolveVendorLanding
} from '../landing'
import type { CurrentUserWorkspace } from '@/types'

/** 造一个只开指定权限档的工作区。 */
function ws(perms: Partial<NonNullable<CurrentUserWorkspace['permissions']>>): CurrentUserWorkspace {
  return {
    id: 7,
    name: 'A 家',
    permissions: {
      account_manage: false,
      group_ops: false,
      group_billing: false,
      proxy_manage: false,
      monitor_view: false,
      ...perms
    }
  } as CurrentUserWorkspace
}

describe('resolveVendorLanding', () => {
  it('按权限档挑第一个可用页面', () => {
    expect(resolveVendorLanding(ws({ account_manage: true }))).toBe('/admin/accounts')
    expect(resolveVendorLanding(ws({ group_ops: true }))).toBe('/admin/groups')
    expect(resolveVendorLanding(ws({ proxy_manage: true }))).toBe('/admin/proxies')
  })

  // 只开计费档也应落到分组页 —— 调价入口在那里。
  it('仅开计费档时落到分组页', () => {
    expect(resolveVendorLanding(ws({ group_billing: true }))).toBe('/admin/groups')
  })

  // 只开监控档时账号列表是只读可见的，与后端 permitAccountRead 同口径。
  it('仅开监控档时落到用量页', () => {
    expect(resolveVendorLanding(ws({ monitor_view: true }))).toBe('/admin/usage')
  })

  // 账号管理排在最前：供应商开工第一件事是看账号池。
  it('多档同开时优先账号页', () => {
    expect(
      resolveVendorLanding(ws({ account_manage: true, proxy_manage: true, monitor_view: true }))
    ).toBe('/admin/accounts')
  })

  // 权限档全关时管理端每页都会 403，把人丢进去只会看到一片报错。
  it('权限档全关回退用户面板', () => {
    expect(resolveVendorLanding(ws({}))).toBe(USER_LANDING)
  })

  // 工作区拉取失败或尚未绑定时不能凭空猜一个管理页。
  it('无工作区回退用户面板', () => {
    expect(resolveVendorLanding(null)).toBe(USER_LANDING)
  })
})

describe('resolveLandingPath', () => {
  it('站长去管理端总览', () => {
    expect(resolveLandingPath({ isOwner: true, isVendor: false, workspace: null })).toBe(
      OWNER_LANDING
    )
  })

  it('普通用户去用户面板', () => {
    expect(resolveLandingPath({ isOwner: false, isVendor: false, workspace: null })).toBe(
      USER_LANDING
    )
  })

  it('vendor 走权限档解析', () => {
    expect(
      resolveLandingPath({
        isOwner: false,
        isVendor: true,
        workspace: ws({ proxy_manage: true })
      })
    ).toBe('/admin/proxies')
  })

  // 深链被拦到登录页的场景：登录后要回到原本想去的地方。
  it('优先采纳请求的重定向目标', () => {
    expect(
      resolveLandingPath({
        isOwner: true,
        isVendor: false,
        workspace: null,
        requestedRedirect: '/admin/accounts?id=3'
      })
    ).toBe('/admin/accounts?id=3')
  })

  // 否则会在登录页自跳，表现为点了登录却停在原地。
  it('忽略指向登录页自身的重定向', () => {
    for (const target of ['/login', '/login?redirect=/login']) {
      expect(
        resolveLandingPath({
          isOwner: false,
          isVendor: false,
          workspace: null,
          requestedRedirect: target
        })
      ).toBe(USER_LANDING)
    }
  })

  it('忽略空白重定向', () => {
    expect(
      resolveLandingPath({
        isOwner: true,
        isVendor: false,
        workspace: null,
        requestedRedirect: '   '
      })
    ).toBe(OWNER_LANDING)
  })
})
