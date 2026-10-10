import { describe, it, expect, vi, afterEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'

// t() 回显 key，便于断言文案键
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

import UserPlatformQuotaCell from '../UserPlatformQuotaCell.vue'
import type { PlatformQuotaItem } from '@/api/admin/users'
import { BUILTIN_PLATFORM_CATALOG, listPlatformIds, resetPlatformCatalog, setPlatformCatalog } from '@/constants/platformCatalog'

function item(over: Partial<PlatformQuotaItem> & { platform: PlatformQuotaItem['platform'] }): PlatformQuotaItem {
  return {
    daily_limit_usd: null, weekly_limit_usd: null, monthly_limit_usd: null,
    daily_usage_usd: 0, weekly_usage_usd: 0, monthly_usage_usd: 0,
    ...over,
  } as PlatformQuotaItem
}

afterEach(() => {
  resetPlatformCatalog()
})

describe('UserPlatformQuotaCell', () => {
  it('quotas 为 undefined 时渲染加载占位 …', () => {
    const w = mount(UserPlatformQuotaCell, { props: { quotas: undefined } })
    expect(w.text()).toContain('…')
    expect(w.html()).not.toContain('admin.users.platformQuota.cellNotConfigured')
  })

  it('空数组渲染「未配置」', () => {
    const w = mount(UserPlatformQuotaCell, { props: { quotas: [] } })
    expect(w.html()).toContain('admin.users.platformQuota.cellNotConfigured')
  })

  it('平台有记录但全部 limit 为 null 时视为未配置', () => {
    const w = mount(UserPlatformQuotaCell, {
      props: { quotas: [item({ platform: 'openai', daily_usage_usd: 5 })] },
    })
    expect(w.html()).toContain('admin.users.platformQuota.cellNotConfigured')
  })

  it('已配置平台渲染 已用/限额，null 档显示 —，金额去尾零', () => {
    const w = mount(UserPlatformQuotaCell, {
      props: {
        quotas: [
          item({ platform: 'anthropic', daily_limit_usd: 100, daily_usage_usd: 30,
                 weekly_limit_usd: null, weekly_usage_usd: 0,
                 monthly_limit_usd: 2000, monthly_usage_usd: 90.5 }),
        ],
      },
    })
    const html = w.html()
    expect(html).toContain('anthropic')
    expect(html).toContain('30/100')
    expect(html).toContain('0/—')
    expect(html).toContain('90.5/2000')
  })

  it('多平台按完整目录顺序展示 OpenCode 和 TypeSafe，且仅展示有限额的平台', () => {
    const w = mount(UserPlatformQuotaCell, {
      props: {
        quotas: [
          item({ platform: 'gemini', monthly_limit_usd: 50 }),
          item({ platform: 'opencode_go', daily_limit_usd: 20 }),
          item({ platform: 'typesafe', daily_limit_usd: 5 }),
          item({ platform: 'anthropic', daily_limit_usd: 10 }),
          item({ platform: 'openai', daily_usage_usd: 9 }),
        ],
      },
    })
    const text = w.text()
    expect(text.indexOf('anthropic')).toBeLessThan(text.indexOf('gemini'))
    expect(text.indexOf('gemini')).toBeLessThan(text.indexOf('opencode_go'))
    expect(text.indexOf('opencode_go')).toBeLessThan(text.indexOf('typesafe'))
    expect(text).not.toContain('openai')
  })

  it('全目录平台的零限额均视为已配置，未知平台排在最后', () => {
    const platforms = listPlatformIds()
    const w = mount(UserPlatformQuotaCell, {
      props: {
        quotas: [
          item({ platform: 'unknown_provider', daily_limit_usd: 0 }),
          ...platforms.slice().reverse().map(platform => item({ platform, daily_limit_usd: 0 })),
        ],
      },
    })
    expect(w.findAll('.font-mono').map(row => row.text())).toEqual([...platforms, 'unknown_provider'])
    expect(w.text()).toContain('0/0')
    expect(w.text()).toContain('0/—')
    w.unmount()
  })

  it('目录新增平台后更新展示顺序，并保持未登记平台在最后', async () => {
    const w = mount(UserPlatformQuotaCell, {
      props: {
        quotas: [
          item({ platform: 'unknown_provider', daily_limit_usd: 1 }),
          item({ platform: 'new_provider', daily_limit_usd: 1 }),
          item({ platform: 'anthropic', daily_limit_usd: 1 }),
        ],
      },
    })
    expect(w.findAll('.font-mono')[0].text()).toBe('anthropic')
    setPlatformCatalog({
      ...BUILTIN_PLATFORM_CATALOG,
      platforms: [
        { id: 'new_provider', display_name: 'New Provider', gateway: 'openai', cn_provider: false },
        ...BUILTIN_PLATFORM_CATALOG.platforms,
      ],
    })
    await nextTick()
    expect(w.findAll('.font-mono').map(row => row.text())).toEqual(['new_provider', 'anthropic', 'unknown_provider'])
    w.unmount()
  })
})
