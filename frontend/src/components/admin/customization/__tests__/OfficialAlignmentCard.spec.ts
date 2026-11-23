import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { createI18n } from 'vue-i18n'
import type { ClaudeCustomizationField } from '@/api/admin/settings'
import enSettings from '@/i18n/locales/en/admin/settings'
import zhSettings from '@/i18n/locales/zh/admin/settings'
import Select from '@/components/common/Select.vue'
import OfficialAlignmentCard from '../OfficialAlignmentCard.vue'
import { alignmentDefaults, alignmentFields } from './alignmentSchema'

function renderCard(overrides: Partial<InstanceType<typeof OfficialAlignmentCard>['$props']> = {}, locale = 'zh') {
  const i18n = createI18n({ legacy: false, locale, messages: { zh: { admin: zhSettings }, en: { admin: enSettings } } })
  return mount(OfficialAlignmentCard, {
    props: { mode: 'global', fields: alignmentFields, modelValue: alignmentDefaults, ...overrides },
    global: { plugins: [i18n], stubs: { teleport: true, Icon: true } },
  })
}

describe('OfficialAlignmentCard', () => {
  it('renders accessible controls in schema order with formal names and no bulk actions', () => {
    const wrapper = renderCard()
    expect(wrapper.get('h3').text()).toBe('官方对齐')
    const rows = wrapper.findAll('[data-field]')
    expect(rows.map(row => row.get('label').text())).toEqual([
      '上下文管理字段缺少 beta 头时的处理', '补全请求 ID', '缓存断点超过 4 个时的处理',
      '未知 anthropic-beta 值的处理', '模型列表格式', '透传官方响应头', '思考块上的 cache_control 处理',
      'Bedrock 兼容渠道保留上下文管理字段',
    ])
    for (const row of rows) {
      const control = row.get('button[aria-haspopup]')
      expect(row.get('label').attributes('for')).toBe(control.attributes('id'))
      expect(row.get(`[id="${control.attributes('aria-describedby')}"]`).text()).not.toBe('')
      expect(row.get('button:not([aria-haspopup])').attributes('aria-controls')).toBeUndefined()
      expect(row.findAll('button')).toHaveLength(2)
      expect(row.findAll('button[aria-haspopup]')).toHaveLength(1)
      expect(row.findAll('button:not([aria-haspopup])').map(button => button.text())).toEqual(['影响评估'])
    }
    expect(wrapper.text()).not.toMatch(/\b[A-C]\d{1,2}\b/)
    expect(wrapper.findAll('button')).toHaveLength(16)
    wrapper.unmount()
  })

  it.each([
    ['zh', 'Bedrock 兼容渠道保留上下文管理字段', '删除字段（默认 · 当前生产行为）', '带 context-management beta 时保留（官方行为）', '继承全局（当前：删除字段）'],
    ['en', 'Keep context_management on Bedrock-compat channels', 'Remove the field (default · current production behavior)', 'Keep with the context-management beta (official behavior)', 'Inherit global (current: Remove the field)'],
  ])('renders the Bedrock switch with opt-in choices and inheritance in %s', async (locale, label, initial, official, inherit) => {
    const wrapper = renderCard({}, locale)
    const row = wrapper.get('[data-field="bedrock_cc_context_management"]')
    expect(row.get('label').text()).toBe(label)
    expect(row.get('button[aria-haspopup]').text()).toBe(initial)
    await row.get('button[aria-haspopup]').trigger('click')
    expect(row.findAll('[role="option"]').map(option => option.text())).toEqual([initial, official])
    await row.get('button:not([aria-haspopup])').trigger('click')
    expect(row.findAll('li')).toHaveLength(5)
    expect(row.get('ul').text()).toContain('400')
    expect(row.text()).toContain('compact-2026-01-12')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    await wrapper.setProps({ mode: 'account', modelValue: {}, globalValues: alignmentDefaults })
    expect(row.get('button[aria-haspopup]').attributes('disabled')).toBeUndefined()
    expect(row.findAll('[role="option"]')[0].text()).toBe(inherit)
    await row.findAll('[role="option"]')[0].trigger('click')
    expect(wrapper.emitted('update:modelValue')).toEqual([[{ bedrock_cc_context_management: null }]])
    wrapper.unmount()
  })

  it('derives option ordering, suffixes and risk from the supplied schema', async () => {
    const field = { ...alignmentFields[2], default: 'passthrough', official_value: 'trim', risk: 'high' as const }
    const wrapper = renderCard({ fields: [field, ...alignmentFields.filter(f => f.key !== field.key)] })
    const rows = wrapper.findAll('[data-field]')
    expect(rows.map(row => row.attributes('data-field'))[0]).toBe(field.key)
    expect(rows.map(row => row.text().match(/成功率：(无影响|低|中|高)/)?.[1])).toEqual(['高', '中', '低', '高', '无影响', '低', '中', '中'])
    await rows[0].get('button[aria-haspopup]').trigger('click')
    expect(rows[0].findAll('[role="option"]').map(option => option.text())).toEqual([
      '自动删除多余断点（官方行为）', '不删除，交给上游处理（默认 · 当前生产行为）', '按官方返回 400',
    ])
    await rows[0].findAll('[role="option"]')[2].trigger('click')
    expect(wrapper.emitted('update:modelValue')).toEqual([[{ ...alignmentDefaults, cache_control_limit_mode: 'reject' }]])
    expect(alignmentDefaults.cache_control_limit_mode).toBe('trim')
    wrapper.unmount()
  })

  it('shows the inherited global value, emits null and locks unavailable account controls', async () => {
    const wrapper = renderCard({
      mode: 'account', fields: alignmentFields.map(f => f.key === 'upstream_header_passthrough' ? { ...f, account_overridable: false } : f),
      globalValues: { ...alignmentDefaults, request_id_mode: 'fill' },
      modelValue: { request_id_mode: 'passthrough', cache_control_limit_mode: 'reject' },
    })
    const row = wrapper.get('[data-field="request_id_mode"]')
    await row.get('button[aria-haspopup]').trigger('click')
    const inherit = row.findAll('[role="option"]')[0]
    expect(inherit.text()).toBe('继承全局（当前：按官方补全）')
    await inherit.trigger('click')
    expect(wrapper.emitted('update:modelValue')).toEqual([[{ request_id_mode: null, cache_control_limit_mode: 'reject' }]])
    await wrapper.setProps({ modelValue: { models_catalog: 'official' } })
    expect(wrapper.get('[data-field="models_catalog"]').getComponent(Select).props('modelValue')).toBeNull()
    expect(wrapper.get('[data-field="models_catalog"]').text()).toContain('此项在选定账号之前生效，只能全局设置')
    expect(wrapper.get('[data-field="upstream_header_passthrough"]').text()).toContain('此项不支持账号级覆写，只能全局设置')
    for (const key of ['models_catalog', 'upstream_header_passthrough']) {
      const control = wrapper.get(`[data-field="${key}"] button[aria-haspopup]`)
      expect(control.attributes('disabled')).toBeDefined()
      const descriptions = control.attributes('aria-describedby').split(' ')
      expect(descriptions).toHaveLength(2)
      expect(wrapper.get(`[id="${descriptions[1]}"]`).text()).toContain('全局设置')
    }
    wrapper.unmount()
  })

  it('distinguishes zero hits, missing counters and an unavailable summary', async () => {
    const wrapper = renderCard({ hits: { unknown_beta_header: { last_24h: 0, last_7d: 23 } } })
    const beta = wrapper.get('[data-field="unknown_beta_header"]')
    expect(beta.text()).toContain('命中统计：近 24 小时 0 次 / 近 7 天 23 次')
    expect(wrapper.get('[data-field="thinking_cache_control"]').text()).toContain('统计不可用')
    for (const key of ['models_catalog', 'request_id_mode', 'upstream_header_passthrough', 'bedrock_cc_context_management']) {
      expect(wrapper.get(`[data-field="${key}"]`).text()).not.toMatch(/统计不可用|近 24 小时/)
    }
    await wrapper.setProps({ hits: null })
    expect(beta.text()).toContain('统计不可用')
    wrapper.unmount()
  })


  it.each([
    ['zh', '此项在选定账号之前生效，只能全局设置'],
    ['en', 'This setting takes effect before account selection and can only be configured globally.'],
  ])('shows the global-only explanation once in %s account mode', (locale, hint) => {
    const wrapper = renderCard({ mode: 'account' }, locale)
    const row = wrapper.get('[data-field="models_catalog"]')
    expect(row.text().split(hint)).toHaveLength(2)
    wrapper.unmount()
  })

  it.each([['zh', '10 秒'], ['en', '10 seconds']])('shows the cross-instance delay for unknown beta recovery in %s', async (locale, delay) => {
    const wrapper = renderCard({}, locale)
    const row = wrapper.get('[data-field="unknown_beta_header"]')
    await row.get('button:not([aria-haspopup])').trigger('click')
    expect(row.findAll('li')[1].text()).toContain(delay)
    wrapper.unmount()
  })

  it('uses the schema default as the selected global value without emitting an edit', async () => {
    const wrapper = renderCard({ modelValue: {}, fields: [alignmentFields[1]] })
    expect(wrapper.getComponent(Select).props('modelValue')).toBe('passthrough')
    expect(wrapper.get('button[aria-haspopup]').text()).toBe('原样透传（默认 · 当前生产行为）')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    wrapper.unmount()
  })

  it.each(['global', 'account'] as const)('falls back to raw field and option names without leaking translation paths in %s mode', async (mode) => {
    const field: ClaudeCustomizationField = {
      ...alignmentFields[1], key: 'future_alignment' as ClaudeCustomizationField['key'],
      risk: 'unknown' as ClaudeCustomizationField['risk'], options: ['future_default', 'future_official'], default: 'future_default', official_value: 'future_official',
    }
    const wrapper = renderCard({ mode, modelValue: {}, fields: [field], globalValues: { future_alignment: 'future_official' } })
    const row = wrapper.get('[data-field="future_alignment"]')
    expect(row.get('label').text()).toBe('future_alignment')
    expect(row.text()).toContain('unknown')
    const control = row.get('button[aria-haspopup]')
    expect(row.get(`[id="${control.attributes('aria-describedby')}"]`).text()).toBe('')
    await control.trigger('click')
    const options = row.findAll('[role="option"]').map(option => option.text())
    expect(options).toEqual(mode === 'account'
      ? ['继承全局（当前：future_official）', 'future_default（默认 · 当前生产行为）', 'future_official（官方行为）']
      : ['future_default（默认 · 当前生产行为）', 'future_official（官方行为）'])
    await row.get('button:not([aria-haspopup])').trigger('click')
    expect(row.findAll('li')).toHaveLength(0)
    expect(wrapper.text()).not.toContain('admin.customization.')
    wrapper.unmount()
  })

  it('falls back for an unknown option on a known field', async () => {
    const wrapper = renderCard({
      fields: [{ ...alignmentFields[1], options: ['future_value'] }],
      modelValue: { request_id_mode: 'future_value' },
    })
    expect(wrapper.get('button[aria-haspopup]').text()).toBe('future_value')
    await wrapper.get('button[aria-haspopup]').trigger('click')
    expect(wrapper.get('[role="option"]').text()).toBe('future_value')
    wrapper.unmount()
  })

  it('separates English hit counts and preserves the scoped impact explanations', async () => {
    const wrapper = renderCard({ hits: { unknown_beta_header: { last_24h: 0, last_7d: 23 } } }, 'en')
    expect(wrapper.get('[data-field="unknown_beta_header"]').text()).toContain('Hit counts: Last 24 hours: 0 / Last 7 days: 23')
    const request = wrapper.get('[data-field="request_id_mode"]')
    await request.get('button:not([aria-haspopup])').trigger('click')
    expect(request.findAll('li')[1].text()).toContain('streaming requests in that group')
    const thinking = wrapper.get('[data-field="thinking_cache_control"]')
    await thinking.get('button:not([aria-haspopup])').trigger('click')
    expect(thinking.findAll('li')[0].text()).toContain('Common sources:')
    for (const key of ['cache_control_limit_mode', 'thinking_cache_control']) {
      const row = wrapper.get(`[data-field="${key}"]`)
      await row.get('button[aria-haspopup]').trigger('click')
      expect(row.findAll('[role="option"]').find(option => option.text() === 'Leave it to the upstream')).toBeDefined()
      if (!row.find('ul').exists()) await row.get('button:not([aria-haspopup])').trigger('click')
      expect(row.get('ul').text()).toContain('“Leave it to the upstream”')
    }
    wrapper.unmount()
  })

  it('opens and closes the impact list for a single row', async () => {
    const wrapper = renderCard()
    const row = wrapper.get('[data-field="request_id_mode"]')
    const button = row.findAll('button').find(b => b.text() === '影响评估')!
    expect(button.attributes('aria-expanded')).toBe('false')
    expect(wrapper.findAll('li')).toHaveLength(0)
    await button.trigger('click')
    expect(button.attributes('aria-expanded')).toBe('true')
    expect(wrapper.findAll('li')).toHaveLength(5)
    expect(button.attributes('aria-controls')).toBeTruthy()
    expect(row.get('ul').attributes('id')).toBe(button.attributes('aria-controls'))
    expect(row.get('ul').text()).toMatch(/不再发送 ping.*用户并发槽位 30 秒.*120 秒.*非粘性默认 30 秒.*用户消息队列默认 30 秒.*叠加.*首字节超时.*断开/)
    await button.trigger('click')
    expect(row.find('ul').exists()).toBe(false)
    await wrapper.setProps({ disabled: true })
    expect(wrapper.findAllComponents(Select).every(select => select.props('disabled'))).toBe(true)
    wrapper.unmount()
  })
})

it('disables an inapplicable override and allows removing the stored value',async()=>{
 const key='context_management_missing_beta'
 const wrapper=renderCard({mode:'account',accountType:'oauth',ignoredOverrides:[key],fields:[{...alignmentFields[0],account_types:['apikey']}],modelValue:{[key]:'reject'},globalValues:{[key]:'strip'}})
 const row=wrapper.get('[data-field="'+key+'"]')
 expect(row.getComponent(Select).props('disabled')).toBe(true)
 expect(row.text()).toContain('全局值 strip')
 await row.get('[data-restore-inheritance]').trigger('click')
 expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([{[key]:null}])
})

it('locks a Bedrock account by schema even without ignored overrides', () => {
  const field = { ...alignmentFields[0], account_types: ['apikey', 'oauth', 'setup-token'] }
  const wrapper = renderCard({ mode: 'account', accountType: 'bedrock', fields: [field], ignoredOverrides: [] })
  const row = wrapper.get('[data-field="' + field.key + '"]')
  expect(row.getComponent(Select).props('disabled')).toBe(true)
  expect(row.text()).toContain('此设置不适用于当前账号类型。')
  wrapper.unmount()
})
