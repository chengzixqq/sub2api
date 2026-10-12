import { defineComponent, h, ref } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import Select from '@/components/common/Select.vue'
import MonitorAppearanceSettings from '../MonitorAppearanceSettings.vue'
import { provideMonitorPreferences } from '../useMonitorPreferences'

vi.mock('vue-i18n', async original => ({ ...await original<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key, locale: { value: 'en' } }) }))
beforeEach(() => localStorage.clear())
afterEach(() => localStorage.clear())
const mountSettings = (identity = ref('user:1:workspace:1')) => mount(defineComponent({ setup() { provideMonitorPreferences(identity); return () => h(MonitorAppearanceSettings, { range: '7d', bucketSeconds: 7200 }) } }))
const saved = (identity = 'user:1:workspace:1') => JSON.parse(localStorage.getItem(identity + ':presentation') || 'null')

describe('history settings UI', () => {
  it('uses the selected preview range rather than forcing the draft to 24h', async () => {
    const range = ref<'90m' | '7d'>('7d')
    const wrapper = mount(defineComponent({ setup() { provideMonitorPreferences(ref('preview')); return () => h(MonitorAppearanceSettings, { preview: true, range: range.value }) } }))
    wrapper.getComponent('[data-testid="pref-history-mode"]').vm.$emit('update:modelValue', 'interval')
    await flushPromises()
    await wrapper.get('[data-testid="pref-history-minutes"]').setValue('30')
    expect(wrapper.text()).toContain('this range requests: 60')
    range.value = '90m'
    await flushPromises()
    expect(wrapper.text()).toContain('this range requests: 30')
    expect(saved('preview')).toBeNull()
    wrapper.unmount()
  })

  it('uses Sub Select and valid explicit apply, with unchanged drafts and truthful loaded intervals', async () => {
    const wrapper = mountSettings()
    const mode = wrapper.getComponent('[data-testid="pref-history-mode"]')
    expect(mode.vm.$options.__name).toBe(Select.__name)
    mode.vm.$emit('update:modelValue', 'interval')
    await flushPromises()
    const input = wrapper.get('[data-testid="pref-history-minutes"]')
    await input.setValue('35')
    expect(saved()).toBeNull()
    expect(wrapper.text()).toContain('this range requests: 60')
    expect(wrapper.get('[data-testid="pref-history-loaded"]').text()).toContain('120 minutes')
    await wrapper.get('form').trigger('submit')
    expect(saved()).toMatchObject({ historyMode: 'interval', historyBucketMinutes: 35 })
    expect(wrapper.text()).toContain('History settings applied')
    wrapper.unmount()
    const reloaded = mountSettings()
    expect((reloaded.get('[data-testid="pref-history-minutes"]').element as HTMLInputElement).value).toBe('35')
    reloaded.unmount()
  })

  it.each(['', '0', '5.5', '43205'])('does not save invalid interval %s or silently rewrite the draft', async value => {
    const wrapper = mountSettings()
    wrapper.getComponent('[data-testid="pref-history-mode"]').vm.$emit('update:modelValue', 'interval')
    await flushPromises()
    const input = wrapper.get('[data-testid="pref-history-minutes"]')
    await input.setValue(value)
    expect(wrapper.get('[role="alert"]').text()).toContain('1 to 43200')
    expect(wrapper.get('[data-testid="pref-history-apply"]').attributes('disabled')).toBeDefined()
    await wrapper.get('form').trigger('submit')
    expect(saved()).toBeNull()
    expect((input.element as HTMLInputElement).value).toBe(value)
    wrapper.unmount()
  })

  it.each(['', '0', '1.5', '721'])('rejects invalid target count %s and explains target versus actual', async value => {
    const wrapper = mountSettings()
    wrapper.getComponent('[data-testid="pref-history-mode"]').vm.$emit('update:modelValue', 'count')
    await flushPromises()
    expect(wrapper.text()).toContain('target is not an exact count')
    await wrapper.get('[data-testid="pref-history-count"]').setValue(value)
    await wrapper.get('form').trigger('submit')
    expect(saved()).toBeNull()
    expect(wrapper.get('[role="alert"]').text()).toContain('1 to 720')
    wrapper.unmount()
  })

  it('isolates preferences and unfinished drafts by account and workspace and preserves them on reload', async () => {
    const identity = ref('user:1:workspace:1')
    const wrapper = mountSettings(identity)
    wrapper.getComponent('[data-testid="pref-history-mode"]').vm.$emit('update:modelValue', 'count')
    await flushPromises()
    await wrapper.get('[data-testid="pref-history-count"]').setValue('72')
    await wrapper.get('form').trigger('submit')
    await wrapper.get('[data-testid="pref-history-count"]').setValue('bad')
    identity.value = 'user:2:workspace:1'
    await flushPromises()
    expect(wrapper.find('[data-testid="pref-history-count"]').exists()).toBe(false)
    expect(saved('user:2:workspace:1')).toBeNull()
    identity.value = 'user:1:workspace:1'
    await flushPromises()
    expect((wrapper.get('[data-testid="pref-history-count"]').element as HTMLInputElement).value).toBe('72')
    identity.value = 'user:1:workspace:2'
    await flushPromises()
    expect(wrapper.find('[data-testid="pref-history-count"]').exists()).toBe(false)
    expect(saved('user:1:workspace:2')).toBeNull()
    wrapper.unmount()
    const reloaded = mountSettings(ref('user:1:workspace:1'))
    expect((reloaded.get('[data-testid="pref-history-count"]').element as HTMLInputElement).value).toBe('72')
    reloaded.unmount()
  })
})
