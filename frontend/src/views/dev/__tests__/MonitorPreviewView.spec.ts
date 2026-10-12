import { shallowMount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, nextTick } from 'vue'
import Select from '@/components/common/Select.vue'
import AdminMonitorDashboard from '@/features/channel-monitor-v2/AdminMonitorDashboard.vue'
import MonitorViewSwitcher from '@/features/channel-monitor-v2/MonitorViewSwitcher.vue'
import ObservationCards from '@/features/channel-monitor-v2/ObservationCards.vue'
import ObservationDetailDrawer from '@/features/channel-monitor-v2/ObservationDetailDrawer.vue'
import MonitorAppearanceSettings from '@/features/channel-monitor-v2/MonitorAppearanceSettings.vue'
import { DEFAULT_MONITOR_PREFERENCES, useMonitorPreferences } from '@/features/channel-monitor-v2/useMonitorPreferences'
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
  localStorage.clear()
})

function mountPreview() {
  wrapper = shallowMount(MonitorPreviewView, {
    attachTo: document.body,
    global: { stubs: { Select: false, MonitorRangeSelector: false, teleport: false }, renderStubDefaultSlot: true },
  })
  return wrapper
}

describe('monitor preview project-native controls', () => {
  it('shows all four time ranges for users and changes the actual preview window', async () => {
    const preview = mountPreview()
    const ranges = preview.findAll('[data-testid="monitor-range-selector"] button')
    expect(ranges).toHaveLength(4)
    for (const [index, minutes] of [90, 1440, 10080, 43200].entries()) {
      await ranges[index]!.trigger('click')
      expect(ranges[index]!.attributes('aria-pressed')).toBe('true')
      const coverage = preview.getComponent(ObservationCards).props('overview').coverage
      expect((Date.parse(coverage.requested_end) - Date.parse(coverage.requested_start)) / 60000).toBe(minutes)
      expect(preview.getComponent(MonitorAppearanceSettings).props('range')).toBe(['90m', '24h', '7d', '30d'][index])
    }
  })

  it.each(['empty', 'loading', 'error'])('keeps the range selector usable in the %s state', async id => {
    const preview = mountPreview()
    preview.getComponent(Select).vm.$emit('update:modelValue', id)
    await nextTick()
    const ranges = preview.findAll('[data-testid="monitor-range-selector"] button')
    expect(ranges).toHaveLength(4)
    await ranges[0]!.trigger('click')
    expect(ranges[0]!.attributes('aria-pressed')).toBe('true')
    expect(preview.getComponent(MonitorAppearanceSettings).props('range')).toBe('90m')
  })

  it.each(['empty', 'loading', 'error'])('keeps the admin preview range accessible in the %s state', async id => {
    const preview = mountPreview()
    await preview.findAll('[role="group"] button')[0]!.trigger('click')
    preview.getComponent(Select).vm.$emit('update:modelValue', id)
    await nextTick()
    const ranges = preview.findAll('[data-testid="monitor-range-selector"] button')
    expect(ranges).toHaveLength(4)
    await ranges[3]!.trigger('click')
    expect(preview.getComponent(MonitorAppearanceSettings).props('range')).toBe('30d')
  })

  it('closes old details on range changes and restores each role default', async () => {
    localStorage.setItem('monitor-preview:user:presentation', JSON.stringify({ defaultRange: '7d', historyMode: 'interval', historyBucketMinutes: 30 }))
    localStorage.setItem('monitor-preview:admin:presentation', JSON.stringify({ defaultRange: '90m' }))
    const preview = mountPreview()
    expect(preview.getComponent(MonitorAppearanceSettings).props('range')).toBe('7d')
    expect(preview.getComponent(ObservationCards).props('overview').coverage.bucket_seconds).toBe(3600)
    const cards = preview.getComponent(ObservationCards)
    const item = cards.props('overview').items[0]
    cards.vm.$emit('bucket', item, { start: item.buckets[0].bucket_start, bucket: item.buckets[0] })
    await nextTick()
    expect(preview.getComponent(ObservationDetailDrawer).props('show')).toBe(true)
    await preview.get('[data-testid="monitor-range-selector"] button').trigger('click')
    expect(preview.getComponent(ObservationDetailDrawer).props('show')).toBe(false)
    expect(preview.getComponent(ObservationDetailDrawer).props('filter')).toMatchObject({ range: '90m', bucket_minutes: 30 })
    const roleButtons = preview.findAll('[role="group"] button')
    await roleButtons[0]!.trigger('click')
    expect(preview.getComponent(AdminMonitorDashboard).props('previewRange')).toBe('90m')
    preview.getComponent(AdminMonitorDashboard).vm.$emit('update:previewRange', '30d')
    await nextTick()
    expect(preview.getComponent(MonitorAppearanceSettings).props('range')).toBe('30d')
    const coverage = preview.getComponent(AdminMonitorDashboard).props('previewOverview').coverage
    expect((Date.parse(coverage.requested_end) - Date.parse(coverage.requested_start)) / 86400000).toBe(30)
    await roleButtons[1]!.trigger('click')
    expect(preview.getComponent(MonitorAppearanceSettings).props('range')).toBe('7d')
    expect(JSON.parse(localStorage.getItem('monitor-preview:user:presentation')!).historyBucketMinutes).toBe(30)
  })

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
    expect(preview.getComponent(ObservationCards).props('overview')).toEqual(getMonitorPreviewScenario('high-latency', 'user', undefined, '24h').overview)
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
    expect(preview.getComponent(AdminMonitorDashboard).props('previewOverview')).toEqual(getMonitorPreviewScenario('card-gallery', 'admin', undefined, '24h').overview)
    expect(preview.findComponent(ObservationCards).exists()).toBe(false)
    await roleButtons[1]!.trigger('click')
    expect(preview.findComponent(AdminMonitorDashboard).exists()).toBe(false)
    expect(preview.getComponent(MonitorViewSwitcher).props('modelValue')).toBe('cards')
    expect(preview.getComponent(ObservationCards).props('overview')).toEqual(getMonitorPreviewScenario('card-gallery', 'user', undefined, '24h').overview)
    expect(preview.getComponent(ObservationCards).props('layout')).toBe('cards')
    expect(preview.getComponent(ObservationDetailDrawer).props('show')).toBe(false)
  })

  it('applies saved sample duration and target count to the preview, including after role switches', async () => {
    localStorage.setItem('monitor-preview:user:presentation', JSON.stringify({ ...DEFAULT_MONITOR_PREFERENCES, historyMode: 'interval', historyBucketMinutes: 30 }))
    const preview = mountPreview()
    let overview = preview.getComponent(ObservationCards).props('overview')
    expect(overview.coverage.bucket_seconds).toBe(1800)
    expect(overview.items[0].buckets).toHaveLength(48)
    expect(preview.getComponent(MonitorAppearanceSettings).props('bucketSeconds')).toBe(1800)
    localStorage.setItem('monitor-preview:admin:presentation', JSON.stringify({ ...DEFAULT_MONITOR_PREFERENCES, historyMode: 'count', historyBarCount: 24 }))
    await preview.findAll('[role="group"] button')[0]!.trigger('click')
    overview = preview.getComponent(AdminMonitorDashboard).props('previewOverview')
    expect(overview.coverage.bucket_seconds).toBe(3600)
    expect(overview.items[0].buckets).toHaveLength(24)
    await preview.findAll('[role="group"] button')[1]!.trigger('click')
    expect(preview.getComponent(ObservationCards).props('overview').coverage.bucket_seconds).toBe(1800)
  })

  it('rebuilds history on preference changes and closes details from the old interval', async () => {
    const preferenceControl = defineComponent({
      setup: () => ({ preferences: useMonitorPreferences() }),
      template: '<button data-testid="set-history-hour" @click="preferences.historyMode = \'count\'; preferences.historyBarCount = 24">Apply</button>',
    })
    wrapper = shallowMount(MonitorPreviewView, { global: { stubs: { MonitorAppearanceSettings: preferenceControl } } })
    const cards = wrapper.getComponent(ObservationCards)
    const item = cards.props('overview').items[0]
    cards.vm.$emit('bucket', item, { start: item.buckets[0].bucket_start, bucket: item.buckets[0] })
    await nextTick()
    expect(wrapper.getComponent(ObservationDetailDrawer).props('show')).toBe(true)
    await wrapper.get('[data-testid="set-history-hour"]').trigger('click')
    expect(cards.props('overview').coverage.bucket_seconds).toBe(3600)
    expect(cards.props('overview').items[0].buckets).toHaveLength(24)
    expect(wrapper.getComponent(ObservationDetailDrawer).props('show')).toBe(false)
  })
})
