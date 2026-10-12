import { mount, type VueWrapper } from '@vue/test-utils'
import { nextTick } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import ObservationTimeline from '../ObservationTimeline.vue'
import ObservationGroupCard from '../ObservationGroupCard.vue'
import { getMonitorPreviewScenario } from '../monitorPreview'

vi.mock('vue-i18n', async importOriginal => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({
    t: (key: string) => key, te: () => false, locale: { value: 'en' },
  }),
}))

const overview = getMonitorPreviewScenario('card-gallery', 'admin').overview!
const item = overview.items[0]!
const mounted: VueWrapper[] = []
function timeline(admin = false) {
  const wrapper = mount(ObservationTimeline, {
    attachTo: document.body,
    props: { coverage: overview.coverage, buckets: item.buckets, variant: 'card', admin },
  })
  mounted.push(wrapper)
  return wrapper
}
const tooltip = () => document.querySelector('[role="tooltip"]')
afterEach(() => {
  mounted.splice(0).forEach(wrapper => wrapper.unmount())
  document.body.innerHTML = ''
  vi.restoreAllMocks()
})

describe('monitor card interaction', () => {
  it('portals the tooltip outside transformed cards without a duplicate native title', async () => {
    const wrapper = timeline()
    const bar = wrapper.get('button')
    expect(bar.attributes('title')).toBeUndefined()
    await bar.trigger('mouseenter')
    expect(tooltip()).not.toBeNull()
    expect(wrapper.element.contains(tooltip())).toBe(false)
    expect(bar.attributes('aria-describedby')).toBe(tooltip()!.id)
    await bar.trigger('mouseleave')
    await vi.waitFor(() => expect(tooltip()).toBeNull())
  })

  it('retains keyboard focus evidence after pointer leave and dismisses it with Escape', async () => {
    const wrapper = timeline()
    const bar = wrapper.get('button')
    await bar.trigger('focus')
    await bar.trigger('mouseenter')
    await bar.trigger('mouseleave')
    expect(tooltip()).not.toBeNull()
    await bar.trigger('keydown', { key: 'Escape' })
    await vi.waitFor(() => expect(tooltip()).toBeNull())
    expect(bar.attributes('aria-describedby')).toBeUndefined()
  })

  it('keeps tooltip and arrow within a narrow viewport at either edge', async () => {
    vi.spyOn(window, 'innerWidth', 'get').mockReturnValue(375)
    const wrapper = timeline()
    const bar = wrapper.get('button')
    const bounds = vi.spyOn(bar.element, 'getBoundingClientRect')
    for (const left of [4, 367]) {
      bounds.mockReturnValue({ left, width: 4, top: 200, bottom: 224 } as DOMRect)
      await bar.trigger('mouseenter')
      const popup = tooltip() as HTMLElement
      const center = Number.parseFloat(popup.style.left)
      expect(center - 160).toBeGreaterThanOrEqual(12)
      expect(center + 160).toBeLessThanOrEqual(363)
      expect(Number.parseFloat(popup.style.getPropertyValue('--tooltip-arrow'))).toBeGreaterThanOrEqual(12)
      expect(Number.parseFloat(popup.style.getPropertyValue('--tooltip-arrow'))).toBeLessThanOrEqual(308)
    }
  })

  it('removes privileged counts immediately when the role changes', async () => {
    const wrapper = timeline(true)
    await wrapper.get('button').trigger('mouseenter')
    expect(tooltip()!.textContent).toContain('observation.requests')
    await wrapper.setProps({ admin: false })
    expect(tooltip()!.textContent).not.toContain('observation.requests')
    expect(tooltip()!.textContent).not.toContain('observation.attempts')
  })

  it('dismisses on scrolling/resizing and preserves bucket selection', async () => {
    const wrapper = timeline()
    const bar = wrapper.get('button')
    for (const event of ['scroll', 'resize']) {
      await bar.trigger('mouseenter')
      window.dispatchEvent(new Event(event))
      await nextTick()
      await vi.waitFor(() => expect(tooltip()).toBeNull())
    }
    await bar.trigger('mouseenter')
    await bar.trigger('click')
    expect(wrapper.emitted('select')?.[0]?.[0]).toMatchObject({ bucket: item.buckets[0] })
    await vi.waitFor(() => expect(tooltip()).toBeNull())
  })

  it('destroys a departing privileged tooltip immediately on a role change', async () => {
    const wrapper = timeline(true)
    const bar = wrapper.get('button')
    await bar.trigger('mouseenter')
    expect(tooltip()!.textContent).toContain('observation.requests')
    await bar.trigger('mouseleave')
    await wrapper.setProps({ admin: false })
    expect(document.body.textContent).not.toContain('observation.requests')
    expect(tooltip()).toBeNull()
  })

  it('expands and collapses the model region without changing its metrics or emitting detail', async () => {
    const wrapper = mount(ObservationGroupCard, {
      props: { item, coverage: overview.coverage },
      global: { stubs: { PlatformIcon: true, Icon: true } },
    })
    mounted.push(wrapper)
    const toggle = wrapper.get('footer button')
    const before = wrapper.get('.card-metrics').text()
    await toggle.trigger('click')
    expect(toggle.attributes('aria-expanded')).toBe('true')
    expect(wrapper.get('.card-models-panel').attributes('id')).toBe(toggle.attributes('aria-controls'))
    expect(wrapper.get('.card-models-panel').text()).toContain(item.models[0]!.model)
    await toggle.trigger('click')
    expect(toggle.attributes('aria-expanded')).toBe('false')
    await vi.waitFor(() => expect(wrapper.find('.card-models-panel').exists()).toBe(false))
    expect(wrapper.get('.card-metrics').text()).toBe(before)
    expect(wrapper.emitted('detail')).toBeUndefined()
  })
})
