import { shallowMount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import Select from '@/components/common/Select.vue'
import AdminMonitorDashboard from '@/features/channel-monitor-v2/AdminMonitorDashboard.vue'
import MonitorViewSwitcher from '@/features/channel-monitor-v2/MonitorViewSwitcher.vue'
import ObservationCards from '@/features/channel-monitor-v2/ObservationCards.vue'
import ObservationDetailDrawer from '@/features/channel-monitor-v2/ObservationDetailDrawer.vue'
import { getMonitorPreviewScenario, MONITOR_PREVIEW_SCENARIOS } from '@/features/channel-monitor-v2/monitorPreview'
import MonitorPreviewView from '../MonitorPreviewView.vue'

vi.mock('vue-i18n', async importOriginal => {
  const { ref } = await import('vue')
  return {
    ...await importOriginal<typeof import('vue-i18n')>(),
    useI18n: () => ({ t: (key: string) => key, te: () => false, locale: ref('en') }),
  }
})

let wrapper: ReturnType<typeof shallowMount>
afterEach(() => {
  wrapper?.unmount()
  document.body.innerHTML = ''
})

function mountPreview() {
  wrapper = shallowMount(MonitorPreviewView, {
    attachTo: document.body,
    global: { stubs: { Select: false, teleport: false }, renderStubDefaultSlot: true },
  })
  return wrapper
}

describe('monitor preview project-native controls', () => {
  it('uses the existing Sub Select menu and keeps scenario selection in sync', async () => {
    const preview = mountPreview()
    const select = preview.getComponent(Select)
    expect(preview.find('select').exists()).toBe(false)
    expect(select.props('modelValue')).toBe('card-gallery')
    expect(select.props('options')).toHaveLength(MONITOR_PREVIEW_SCENARIOS.length)
    expect(preview.get('label[for="monitor-preview-scenario"]').exists()).toBe(true)
    await select.get('button').trigger('click')
    const options = Array.from(document.querySelectorAll<HTMLElement>('[role="option"]'))
    const slow = options.find(option => option.textContent?.includes('scenarios.high-latency.label'))!
    expect(document.querySelector('.select-dropdown-portal[role="listbox"]')).not.toBeNull()
    slow.click()
    await nextTick()
    expect(select.props('modelValue')).toBe('high-latency')
    expect(select.get('button').attributes('aria-expanded')).toBe('false')
    expect(preview.getComponent(ObservationCards).props('overview')).toEqual(getMonitorPreviewScenario('high-latency', 'user').overview)
    expect(preview.findAll('[role="listitem"]').find(button => button.text().includes('scenarios.high-latency.label'))?.attributes('aria-pressed')).toBe('true')
  })

  it('retains project Select keyboard selection and Escape focus behavior', async () => {
    const preview = mountPreview()
    const trigger = preview.getComponent(Select).get<HTMLButtonElement>('button')
    await trigger.trigger('keydown', { key: 'ArrowDown' })
    await nextTick()
    const menu = document.querySelector<HTMLElement>('[role="listbox"]')!
    expect(document.activeElement).toBe(menu)
    menu.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }))
    menu.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await nextTick()
    expect(preview.getComponent(Select).props('modelValue')).toBe('normal')
    expect(document.activeElement).toBe(trigger.element)
    await trigger.trigger('click')
    await nextTick()
    document.querySelector('[role="listbox"]')!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await nextTick()
    expect(trigger.attributes('aria-expanded')).toBe('false')
    expect(document.activeElement).toBe(trigger.element)
  })

  it('keeps fixture roles separate and resets the user view when switching back', async () => {
    const preview = mountPreview()
    preview.getComponent(MonitorViewSwitcher).vm.$emit('update:modelValue', 'matrix')
    await nextTick()
    const roleButtons = preview.findAll('[role="group"] button')
    await roleButtons[0]!.trigger('click')
    expect(preview.getComponent(AdminMonitorDashboard).props('previewOverview')).toEqual(getMonitorPreviewScenario('card-gallery', 'admin').overview)
    expect(preview.findComponent(ObservationCards).exists()).toBe(false)
    await roleButtons[1]!.trigger('click')
    expect(preview.findComponent(AdminMonitorDashboard).exists()).toBe(false)
    expect(preview.getComponent(MonitorViewSwitcher).props('modelValue')).toBe('cards')
    expect(preview.getComponent(ObservationCards).props('overview')).toEqual(getMonitorPreviewScenario('card-gallery', 'user').overview)
    expect(preview.getComponent(ObservationCards).props('layout')).toBe('cards')
    expect(preview.getComponent(ObservationDetailDrawer).props('show')).toBe(false)
  })
})
