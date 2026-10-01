import { flushPromises, shallowMount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import ChannelMonitorView from '../ChannelMonitorView.vue'

const list = vi.hoisted(() => vi.fn().mockResolvedValue({ items: [], total: 0 }))
vi.mock('@/utils/featureFlags', () => ({ isChannelMonitorV1Mode: () => true }))
vi.mock('@/api/admin', () => ({ adminAPI: { channelMonitor: { list } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess: vi.fn(), showError: vi.fn() }) }))
vi.mock('vue-i18n', async importOriginal => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))

describe('unified monitor management entry', () => {
  it('opens the monitor dashboard and keeps settings and legacy loading on demand', async () => {
    const wrapper = shallowMount(ChannelMonitorView, { global: { stubs: { AppLayout: { template: '<main><slot /></main>' } } } })
    await flushPromises()
    expect(wrapper.get('[role="tab"]').attributes('aria-selected')).toBe('true')
    expect(wrapper.find('admin-monitor-dashboard-stub').exists()).toBe(true)
    expect(wrapper.find('admin-monitor-settings-drawer-stub').attributes('show')).toBe('false')
    await wrapper.findAll('button').find(button => button.text() === 'channelMonitorV2.unified.settings.title')!.trigger('click')
    expect(wrapper.find('admin-monitor-settings-drawer-stub').attributes('show')).toBe('true')
    expect(list).not.toHaveBeenCalled()
    await wrapper.findAll('[role="tab"]')[1].trigger('click')
    await flushPromises()
    expect(list).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })
})
