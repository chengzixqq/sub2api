import { afterEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'

import en from '../locales/en'
import zh from '../locales/zh'

// Keep the real translation runtime and locale loaders. Only isolate the page-title
// dependencies that setLocale also updates, so this test does not need an API session.
vi.mock('@/router/title', () => ({ resolveRouteDocumentTitle: () => 'Sub2API' }))
vi.mock('@/router', () => ({ default: { currentRoute: { value: {} } } }))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ siteName: 'Sub2API', cachedPublicSettings: null })
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ isAdmin: false }) }))
vi.mock('@/stores/adminSettings', () => ({ useAdminSettingsStore: () => ({ customMenuItems: [] }) }))

type Locale = 'zh' | 'en'
type TranslationCase = [key: string, parameters: Record<string, string | number>, expected: string]

const cases: Record<Locale, TranslationCase[]> = {
  zh: [
    ['channelMonitorV2.filters.labelValue', { label: '平台', value: '全部' }, '平台：全部'],
    ['common.autoRefresh.countdown', { seconds: 30 }, '自动刷新: 30s'],
    ['admin.accounts.autoRefreshCountdown', { seconds: 30 }, '自动刷新：30s'],
    ['admin.accounts.modelCount', { count: 7 }, '7 个模型'],
    ['admin.accounts.selectedModels', { count: 7 }, '已选择 7 个模型'],
    ['pagination.pageOf', { page: 2, total: 10 }, '第 2 / 10 页'],
    ['admin.accounts.capacity.rpm.tieredStickyOnly', { buffer: 12 }, 'RPM 限制 (三区模型) - 仅粘性会话 | 缓冲区: 12'],
    ['admin.accounts.capacity.rpm.tieredBlocked', { buffer: 12 }, 'RPM 限制 (三区模型) - 已阻塞 | 缓冲区: 12'],
    ['admin.accounts.headerOverride.invalidName', {}, "请求头名称格式不正确（仅允许字母、数字和 !#$%&'*+-.^_`|~ 字符）"]
  ],
  en: [
    ['channelMonitorV2.filters.labelValue', { label: 'Platform', value: 'All' }, 'Platform: All'],
    ['common.autoRefresh.countdown', { seconds: 30 }, 'Auto refresh: 30s'],
    ['admin.accounts.autoRefreshCountdown', { seconds: 30 }, 'Auto refresh: 30s'],
    ['admin.accounts.modelCount', { count: 7 }, '7 models'],
    ['admin.accounts.selectedModels', { count: 7 }, 'Selected 7 model(s)'],
    ['pagination.pageOf', { page: 2, total: 10 }, 'Page 2 of 10'],
    ['admin.accounts.capacity.rpm.tieredStickyOnly', { buffer: 12 }, 'RPM limit (Tiered) - Sticky only | Buffer: 12'],
    ['admin.accounts.capacity.rpm.tieredBlocked', { buffer: 12 }, 'RPM limit (Tiered) - Blocked | Buffer: 12'],
    ['admin.accounts.headerOverride.invalidName', {}, "Invalid header name (only letters, digits and !#$%&'*+-.^_`|~ are allowed)"]
  ]
}

describe.each(['zh', 'en'] as const)('real %s runtime translations', (locale) => {
  const i18n = createI18n({ legacy: false, locale, messages: { zh, en }, warnHtmlMessage: false })

  it.each(cases[locale])('renders %s with its complete interpolated text', (key, parameters, expected) => {
    expect(i18n.global.t(key, parameters)).toBe(expected)
  })
})

describe('runtime message syntax', () => {
  it('compiles raw named, list, plural and literal messages without precompiled functions', () => {
    const i18n = createI18n({
      legacy: false,
      locale: 'en',
      messages: {
        en: {
          named: 'Hello {name}',
          list: 'Hello {0}',
          plural: 'No models | One model | {count} models',
          literal: "{'{'}value{'}'} {'|'} user{'@'}example.com"
        }
      }
    })

    expect(i18n.global.t('named', { name: 'Alice' })).toBe('Hello Alice')
    expect(i18n.global.t('list', ['Alice'])).toBe('Hello Alice')
    expect(i18n.global.t('plural', 0)).toBe('No models')
    expect(i18n.global.t('plural', 1)).toBe('One model')
    expect(i18n.global.t('plural', 7)).toBe('7 models')
    expect(i18n.global.t('literal')).toBe('{value} | user@example.com')
    i18n.dispose()
  })
})

describe('application locale loading', () => {
  afterEach(() => {
    localStorage.removeItem('sub2api_locale')
    document.documentElement.removeAttribute('lang')
  })

  it('interpolates after lazy initialization and after switching languages', async () => {
    vi.resetModules()
    localStorage.setItem('sub2api_locale', 'zh')
    const { i18n, initI18n, setLocale, getLocale } = await import('..')

    expect(i18n.global.availableLocales).toEqual([])
    await initI18n()
    expect(getLocale()).toBe('zh')
    expect(document.documentElement.lang).toBe('zh')
    expect(i18n.global.availableLocales).toEqual(['zh'])
    expect(i18n.global.t('admin.accounts.selectedModels', { count: 7 })).toBe('已选择 7 个模型')

    await setLocale('en')
    expect(getLocale()).toBe('en')
    expect(localStorage.getItem('sub2api_locale')).toBe('en')
    expect(document.documentElement.lang).toBe('en')
    expect(i18n.global.availableLocales).toEqual(['en', 'zh'])
    expect(i18n.global.t('admin.accounts.selectedModels', { count: 7 })).toBe('Selected 7 model(s)')

    await setLocale('zh')
    expect(i18n.global.t('admin.accounts.selectedModels', { count: 2 })).toBe('已选择 2 个模型')
    expect(i18n.global.availableLocales).toEqual(['en', 'zh'])
    i18n.dispose()
  })
})
