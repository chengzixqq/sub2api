import { describe, it, expect, vi, afterEach } from 'vitest'
import { mount, type VueWrapper } from '@vue/test-utils'
import { nextTick } from 'vue'

// t() 回显 key；带参数时附上 JSON，便于断言计数
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params ? `${key}:${JSON.stringify(params)}` : key,
    }),
  }
})

import UserDashboardStats from '../UserDashboardStats.vue'
import type { UserDashboardStats as UserStatsType, PlatformDashboardStats } from '@/api/usage'
import type { PlatformQuotaItem } from '@/types'
import { BUILTIN_PLATFORM_CATALOG, listPlatformIds, resetPlatformCatalog, setPlatformCatalog } from '@/constants/platformCatalog'

function makeStats(over: Partial<UserStatsType> = {}): UserStatsType {
  return {
    total_api_keys: 1,
    active_api_keys: 1,
    total_requests: 0,
    total_input_tokens: 0,
    total_output_tokens: 0,
    total_cache_creation_tokens: 0,
    total_cache_read_tokens: 0,
    total_tokens: 0,
    total_cost: 0,
    total_actual_cost: 0,
    today_requests: 0,
    today_input_tokens: 0,
    today_output_tokens: 0,
    today_cache_creation_tokens: 0,
    today_cache_read_tokens: 0,
    today_tokens: 0,
    today_cost: 0,
    today_actual_cost: 0,
    average_duration_ms: 0,
    rpm: 0,
    tpm: 0,
    by_platform: [],
    ...over,
  }
}

function usage(platform: string, cost: number): PlatformDashboardStats {
  return {
    platform,
    total_requests: 1,
    total_tokens: 10,
    total_actual_cost: cost,
    today_requests: 1,
    today_tokens: 10,
    today_actual_cost: cost,
  }
}

function quota(over: Partial<PlatformQuotaItem> & { platform: string }): PlatformQuotaItem {
  return {
    daily_limit_usd: null,
    weekly_limit_usd: null,
    monthly_limit_usd: null,
    daily_usage_usd: 0,
    weekly_usage_usd: 0,
    monthly_usage_usd: 0,
    ...over,
  } as PlatformQuotaItem
}

function mountStats(stats: UserStatsType, platformQuotas: PlatformQuotaItem[] | null = null, isSimple = false) {
  return mount(UserDashboardStats, {
    props: { stats, balance: 0, isSimple, platformQuotas },
    global: { stubs: { Icon: true } },
  })
}

/** 渲染出的平台卡片，按 DOM 顺序返回 data-platform */
function cardPlatforms(w: VueWrapper): string[] {
  return w.findAll('[data-testid="platform-card"]').map((c) => c.attributes('data-platform') ?? '')
}

afterEach(() => {
  resetPlatformCatalog()
})

describe('UserDashboardStats 按平台拆分', () => {
  it('只有用量的平台才产生卡片；三档全空的限额记录不产生卡片', () => {
    const w = mountStats(
      makeStats({ total_actual_cost: 0.03, today_actual_cost: 0.03, by_platform: [usage('grok', 0.03)] }),
      [
        quota({ platform: 'anthropic' }),
        quota({ platform: 'openai' }),
        quota({ platform: 'gemini' }),
        quota({ platform: 'grok' }),
      ]
    )
    expect(cardPlatforms(w)).toEqual(['grok'])
    expect(w.text()).toContain('dashboard.platformCount:{"count":1}')
    expect(w.html()).not.toContain('dashboard.platformQuota.title')
  })

  it('配置了限额但没有用量的平台也产生卡片，并渲染配额区', () => {
    const w = mountStats(
      makeStats({ total_actual_cost: 0.03, today_actual_cost: 0.03, by_platform: [usage('grok', 0.03)] }),
      [quota({ platform: 'openai', daily_limit_usd: 10, daily_usage_usd: 2.5 }), quota({ platform: 'anthropic' })]
    )
    // 固定顺序：openai 排在 grok 前
    expect(cardPlatforms(w)).toEqual(['openai', 'grok'])
    expect(w.text()).toContain('dashboard.platformQuota.title')
    expect(w.text()).toContain('dashboard.platformCount:{"count":2}')
  })

  it('同一平台既有用量又有限额只产生一张卡片', () => {
    const w = mountStats(
      makeStats({ total_actual_cost: 1, today_actual_cost: 1, by_platform: [usage('openai', 1)] }),
      [quota({ platform: 'openai', daily_limit_usd: 10, daily_usage_usd: 1 })]
    )
    expect(cardPlatforms(w)).toEqual(['openai'])
    expect(w.text()).toContain('dashboard.platformQuota.title')
  })

  it('限额为 0 的平台视为已配置，渲染禁用态', () => {
    const w = mountStats(makeStats(), [quota({ platform: 'gemini', weekly_limit_usd: 0 })])
    expect(cardPlatforms(w)).toEqual(['gemini'])
    expect(w.text()).toContain('dashboard.platformQuota.disabled')
    expect(w.text()).toContain('dashboard.platformCount:{"count":1}')
  })

  it('完整平台目录按固定顺序排序，并保留 OpenCode 标签', () => {
    const w = mountStats(
      makeStats({ total_actual_cost: 0.8, today_actual_cost: 0, by_platform: [usage('opencode_go', 0.3), usage('kimi', 0.3), usage('anthropic', 0.2)] })
    )
    expect(cardPlatforms(w)).toEqual(['anthropic', 'kimi', 'opencode_go'])
    expect(w.text()).toContain('Kimi')
    expect(w.text()).toContain('OpenCode')
  })

  it('TypeSafe 使用 Jev 标签并位于未知平台之前', () => {
    const w = mountStats(
      makeStats({ total_actual_cost: 0.5, today_actual_cost: 0, by_platform: [usage('unknown_provider', 0.3), usage('typesafe', 0.2)] })
    )
    expect(cardPlatforms(w)).toEqual(['typesafe', 'unknown_provider'])
    expect(w.text()).toContain('TypeSafe / Jev')
  })

  it('完整目录平台按目录顺序展示并保留新旧平台标签', () => {
    const platforms = listPlatformIds()
    const w = mountStats(makeStats({
      total_actual_cost: platforms.length,
      today_actual_cost: platforms.length,
      by_platform: platforms.slice().reverse().map(platform => usage(platform, 1)),
    }))
    expect(cardPlatforms(w)).toEqual(platforms)
    expect(w.text()).toContain('OpenCode')
    expect(w.text()).toContain('TypeSafe / Jev')
    expect(w.text()).toContain('Command Code')
    expect(w.text()).toContain('Cline')
    w.unmount()
  })

  it('动态目录更新平台顺序和标签，未知平台仍按名称排在最后', async () => {
    const w = mountStats(makeStats({
      total_actual_cost: 4,
      today_actual_cost: 4,
      by_platform: [usage('z_unknown', 1), usage('new_provider', 1), usage('anthropic', 1), usage('a_unknown', 1)],
    }))
    expect(cardPlatforms(w)).toEqual(['anthropic', 'a_unknown', 'new_provider', 'z_unknown'])
    setPlatformCatalog({
      ...BUILTIN_PLATFORM_CATALOG,
      platforms: [
        { id: 'new_provider', display_name: 'New Provider', gateway: 'openai', cn_provider: false },
        ...BUILTIN_PLATFORM_CATALOG.platforms,
      ],
    })
    await nextTick()
    expect(cardPlatforms(w)).toEqual(['new_provider', 'anthropic', 'a_unknown', 'z_unknown'])
    expect(w.text()).toContain('New Provider')
    expect(w.text()).toContain('Claude')
    w.unmount()
  })

  it('总值大于各平台之和时追加"其他"卡片，且不计入平台计数', () => {
    const w = mountStats(
      makeStats({ total_actual_cost: 1.0, today_actual_cost: 0, by_platform: [usage('anthropic', 0.4)] })
    )
    expect(cardPlatforms(w)).toEqual(['anthropic', '__other__'])
    expect(w.text()).toContain('dashboard.platformOther')
    expect(w.text()).toContain('dashboard.platformCount:{"count":1}')
  })

  it('没有任何用量也没有配置限额时不渲染整块', () => {
    const w = mountStats(makeStats(), [quota({ platform: 'anthropic' }), quota({ platform: 'openai' })])
    expect(w.html()).not.toContain('dashboard.platformBreakdown')
    expect(cardPlatforms(w)).toEqual([])
  })

  it('简易模式不渲染整块', () => {
    const w = mountStats(makeStats({ by_platform: [usage('openai', 1)] }), null, true)
    expect(w.html()).not.toContain('dashboard.platformBreakdown')
  })
})
