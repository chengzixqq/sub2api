import { flushPromises, shallowMount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import ChannelMonitorView from '../ChannelMonitorView.vue'

const list = vi.hoisted(() => vi.fn().mockResolvedValue({ items: [], total: 0 }))
vi.mock('@/utils/featureFlags', () => ({ isChannelMonitorV1Mode: () => true }))
vi.mock('@/api/admin', () => ({ adminAPI: { channelMonitor: { list } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: vi.fn(), showError: vi.fn() }) }))
vi.mock('vue-i18n', async importOriginal => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))

describe('unified monitor management entry', () => {
  it('opens unified policy before legacy mode settings are loaded', async () => {
    const wrapper = shallowMount(ChannelMonitorView, { global: { stubs: { AppLayout: { template: '<main><slot /></main>' } } } })
    await flushPromises()
    expect(wrapper.get('[role="tab"]').attributes('aria-selected')).toBe('true')
    expect(wrapper.find('observation-settings-panel-stub').exists()).toBe(true)
    expect(list).not.toHaveBeenCalled()
    await wrapper.findAll('[role="tab"]')[1].trigger('click')
    await flushPromises()
    expect(list).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })
})
