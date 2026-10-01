import { flushPromises, shallowMount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import AdminMonitorSettingsDrawer from '../AdminMonitorSettingsDrawer.vue'
vi.mock('vue-i18n', async importOriginal => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key, locale: { value: 'en' } }) }))

describe('monitor settings drawer', () => {
  it('mounts settings only after opening and selects one configuration section at a time', async () => {
    const wrapper = shallowMount(AdminMonitorSettingsDrawer, { props: { show: false }, global: { stubs: { Teleport: true } } })
    expect(wrapper.find('observation-settings-panel-stub').exists()).toBe(false)
    expect(wrapper.find('monitor-probe-settings-stub').exists()).toBe(false)
    expect(wrapper.find('monitor-settings-panel-stub').exists()).toBe(false)
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(wrapper.find('observation-settings-panel-stub').exists()).toBe(true)
    expect(wrapper.find('monitor-probe-settings-stub').exists()).toBe(false)
    await wrapper.findAll('nav button')[1].trigger('click')
    expect(wrapper.find('observation-settings-panel-stub').exists()).toBe(false)
    expect(wrapper.find('monitor-probe-settings-stub').exists()).toBe(true)
    await wrapper.findAll('nav button')[2].trigger('click')
    expect(wrapper.find('monitor-settings-panel-stub').exists()).toBe(true)
    await wrapper.setProps({ show: false })
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    wrapper.unmount()
  })
})
