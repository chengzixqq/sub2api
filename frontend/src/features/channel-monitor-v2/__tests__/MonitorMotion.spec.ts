import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ObservationChannel } from '@/api/channelMonitorV2'
import ObservationDetailDrawer from '../ObservationDetailDrawer.vue'
import MonitorViewSwitcher from '../MonitorViewSwitcher.vue'

const api = vi.hoisted(() => ({ getMonitorAccountDetail: vi.fn() }))
vi.mock('@/api/channelMonitorV2', () => api)
vi.mock('vue-i18n', async importOriginal => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key, te: () => false, locale: { value: 'en' } }),
}))
const item = {
  group_id: 1, group_name: 'Motion channel', models: [],
  metrics: { reliability_rate: 1, sample_state: 'sufficient', ttft: { p50_ms: 300, p95_ms: 500 }, cache_rate: null },
  health: { reliability: 'healthy', latency: 'healthy' },
} as unknown as ObservationChannel
const props = {
  show: true, item, selection: null,
  filter: { range: '24h' as const, platforms: [], groupIds: [], models: [] },
  identity: 'admin:1', admin: true, owner: true,
}
const wrappers: ReturnType<typeof mount>[] = []
function mountDrawer(overrides = {}) {
  const wrapper = mount(ObservationDetailDrawer, {
    props: { ...props, ...overrides }, attachTo: document.body,
    global: { stubs: { transition: false, Icon: true } },
  })
  wrappers.push(wrapper)
  return wrapper
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.useFakeTimers()
  api.getMonitorAccountDetail.mockResolvedValue({ items: [{ account_id: 99, metrics: {} }], samples: [] })
})
afterEach(() => {
  wrappers.forEach(wrapper => wrapper.unmount())
  wrappers.length = 0
  document.body.innerHTML = ''
  vi.useRealTimers()
})

describe('monitor interaction motion', () => {
  it('returns focus and makes a closing animated drawer non-interactive', async () => {
    const opener = document.createElement('button')
    document.body.append(opener)
    opener.focus()
    const wrapper = mountDrawer()
    await flushPromises()
    expect(document.activeElement).toBe(document.querySelector('[role="dialog"]'))
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    expect(wrapper.emitted('close')).toHaveLength(1)
    await wrapper.setProps({ show: false })
    expect(document.activeElement).toBe(opener)
    const leaving = document.querySelector('.monitor-detail-leave-active')
    expect(leaving?.hasAttribute('inert')).toBe(true)
    expect(leaving?.getAttribute('aria-hidden')).toBe('true')
  })

  it('removes any leaving privileged snapshot immediately on identity change', async () => {
    const wrapper = mountDrawer()
    await flushPromises()
    expect(document.body.textContent).toContain('#99')
    await wrapper.setProps({ show: false })
    expect(document.querySelector('.monitor-detail-leave-active')).not.toBeNull()
    await wrapper.setProps({ identity: 'user:2', admin: false, owner: false, item: null })
    expect(document.body.textContent).not.toContain('#99')
    expect(document.querySelector('[role="dialog"]')).toBeNull()
  })

  it('aborts an account request without waiting for the exit animation', async () => {
    api.getMonitorAccountDetail.mockImplementation(() => new Promise(() => {}))
    const wrapper = mountDrawer()
    await flushPromises()
    const signal = api.getMonitorAccountDetail.mock.calls[0][2] as AbortSignal
    expect(signal.aborted).toBe(false)
    await wrapper.setProps({ show: false })
    expect(signal.aborted).toBe(true)
  })

  it('keeps view selection immediate while using the animated button chrome', async () => {
    const wrapper = mount(MonitorViewSwitcher, { props: {
      modelValue: 'cards', label: 'Views', items: [
        { value: 'cards', label: 'Cards' }, { value: 'matrix', label: 'Matrix' },
      ],
    } })
    wrappers.push(wrapper)
    const matrix = wrapper.findAll('[role="tab"]')[1]
    await matrix.trigger('click')
    expect(wrapper.emitted('update:modelValue')).toEqual([['matrix']])
    await wrapper.setProps({ modelValue: 'matrix' })
    expect(matrix.attributes('aria-selected')).toBe('true')
    expect(matrix.classes()).toContain('monitor-view-tab')
  })
})
