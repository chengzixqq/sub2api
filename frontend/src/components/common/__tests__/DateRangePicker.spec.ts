import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { ref } from 'vue'

import DateRangePicker from '../DateRangePicker.vue'

const messages: Record<string, string> = {
  'dates.today': 'Today',
  'dates.yesterday': 'Yesterday',
  'dates.last24Hours': 'Last 24 Hours',
  'dates.last7Days': 'Last 7 Days',
  'dates.last14Days': 'Last 14 Days',
  'dates.last30Days': 'Last 30 Days',
  'dates.thisMonth': 'This Month',
  'dates.lastMonth': 'Last Month',
  'dates.startDate': 'Start Date',
  'dates.endDate': 'End Date',
  'dates.apply': 'Apply',
  'dates.selectDateRange': 'Select date range'
}

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => messages[key] ?? key,
    locale: ref('en')
  })
}))

const formatLocalDate = (date: Date): string => {
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

describe('DateRangePicker', () => {
  afterEach(() => vi.useRealTimers())
  it('uses last 24 hours as the default recognized preset', () => {
    const now = new Date()
    const yesterday = new Date(now.getTime() - 24 * 60 * 60 * 1000)

    const wrapper = mount(DateRangePicker, {
      props: {
        startDate: formatLocalDate(yesterday),
        endDate: formatLocalDate(now)
      },
      global: {
        stubs: {
          Icon: true
        }
      }
    })

    expect(wrapper.text()).toContain('Last 24 Hours')
  })

  it('emits range updates with last24Hours preset when applied', async () => {
    const now = new Date()
    const today = formatLocalDate(now)

    const wrapper = mount(DateRangePicker, {
      props: {
        startDate: today,
        endDate: today
      },
      global: {
        stubs: {
          Icon: true
        }
      }
    })

    await wrapper.find('.date-picker-trigger').trigger('click')
    const presetButton = wrapper.findAll('.date-picker-preset').find((node) =>
      node.text().includes('Last 24 Hours')
    )
    expect(presetButton).toBeDefined()

    await presetButton!.trigger('click')
    await wrapper.find('.date-picker-apply').trigger('click')

    const nowAfterClick = new Date()
    const yesterdayAfterClick = new Date(nowAfterClick.getTime() - 24 * 60 * 60 * 1000)
    const expectedStart = formatLocalDate(yesterdayAfterClick)
    const expectedEnd = formatLocalDate(nowAfterClick)

    expect(wrapper.emitted('update:startDate')?.[0]).toEqual([expectedStart])
    expect(wrapper.emitted('update:endDate')?.[0]).toEqual([expectedEnd])
    expect(wrapper.emitted('change')?.[0]).toEqual([
      {
        startDate: expectedStart,
        endDate: expectedEnd,
        preset: 'last24Hours'
      }
    ])
  })

  it('keeps datetime bounds optional and supports clearing without changing date-only defaults', async () => {
    const wrapper = mount(DateRangePicker, {
      props: { startDate: '', endDate: '', includeTime: true, clearable: true, placeholder: 'All time' }
    })
    expect(wrapper.text()).toContain('All time')
    await wrapper.get('.date-picker-trigger').trigger('click')
    expect(wrapper.findAll('input[type="datetime-local"]')).toHaveLength(2)
    await wrapper.findAll('input')[1].setValue('2026-08-11T18:37')
    await wrapper.get('.date-picker-apply').trigger('click')
    expect(wrapper.emitted('change')?.[0]).toEqual([{ startDate: '', endDate: '2026-08-11T18:37', preset: null }])

    await wrapper.setProps({ endDate: '2026-08-11T18:37' })
    await wrapper.get('.date-picker-trigger').trigger('click')
    await wrapper.get('.date-picker-clear').trigger('click')
    await wrapper.get('.date-picker-apply').trigger('click')
    expect(wrapper.emitted('change')?.[1]).toEqual([{ startDate: '', endDate: '', preset: null }])
    wrapper.unmount()
  })
  it('requires both datetime bounds only for required-range consumers', async () => {
    const wrapper = mount(DateRangePicker, { props: { startDate: '2026-08-11T18:00', endDate: '2026-08-11T19:00', includeTime: true, requiredRange: true } })
    await wrapper.get('.date-picker-trigger').trigger('click')
    await wrapper.findAll('input')[0].setValue('')
    expect(wrapper.get('.date-picker-apply').attributes('disabled')).toBeDefined()
    await wrapper.get('.date-picker-apply').trigger('click')
    expect(wrapper.emitted('change')).toBeUndefined()
    wrapper.unmount()
  })
  it('displays offset-bearing instants locally without changing their original offset', async () => {
    const startDate = '2026-11-01T01:10:00-04:00'
    const endDate = '2026-11-01T01:10:00-05:00'
    const wrapper = mount(DateRangePicker, { props: { startDate, endDate, includeTime: true, requiredRange: true } })
    await wrapper.get('.date-picker-trigger').trigger('click')
    expect((wrapper.findAll('input')[0].element as HTMLInputElement).value).not.toBe('')
    expect(wrapper.get('.date-picker-apply').attributes('disabled')).toBeUndefined()
    await wrapper.get('.date-picker-apply').trigger('click')
    expect(wrapper.emitted('change')?.[0]).toEqual([{ startDate, endDate, preset: null }])
    wrapper.unmount()
  })

  it.each([
    ['Today', '2026-08-11T00:00', '2026-08-12T00:00'],
    ['Yesterday', '2026-08-10T00:00', '2026-08-11T00:00'],
    ['Last 24 Hours', '2026-08-10T17:25', '2026-08-11T17:25'],
    ['Last 7 Days', '2026-08-05T00:00', '2026-08-12T00:00'],
    ['Last Month', '2026-07-01T00:00', '2026-08-01T00:00']
  ])('preserves exclusive datetime boundaries for %s', async (label, start, end) => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-08-11T17:25:00'))
    const wrapper = mount(DateRangePicker, { props: { startDate: '', endDate: '', includeTime: true } })
    await wrapper.get('.date-picker-trigger').trigger('click')
    await wrapper.findAll('.date-picker-preset').find((button) => button.text() === label)!.trigger('click')
    await wrapper.get('.date-picker-apply').trigger('click')
    expect(wrapper.emitted('update:startDate')?.[0]).toEqual([start])
    expect(wrapper.emitted('update:endDate')?.[0]).toEqual([end])
    wrapper.unmount()
  })

  it('blocks inverted and equal datetime ranges before emitting', async () => {
    const wrapper = mount(DateRangePicker, { props: { startDate: '', endDate: '', includeTime: true } })
    await wrapper.get('.date-picker-trigger').trigger('click')
    const inputs = wrapper.findAll('input')
    await inputs[0].setValue('2026-08-11T18:00')
    await inputs[1].setValue('2026-08-11T17:00')
    expect(wrapper.get('.date-picker-apply').attributes('disabled')).toBeDefined()
    await wrapper.get('.date-picker-apply').trigger('click')
    expect(wrapper.emitted('change')).toBeUndefined()
    await inputs[1].setValue('2026-08-11T18:00')
    expect(wrapper.get('.date-picker-apply').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('discards unapplied datetime edits when dismissed or reopened', async () => {
    const wrapper = mount(DateRangePicker, {
      props: { startDate: '', endDate: '', includeTime: true, placeholder: 'All time' },
      attachTo: document.body
    })
    await wrapper.get('.date-picker-trigger').trigger('click')
    await wrapper.findAll('input')[0].setValue('2026-08-11T18:00')
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await wrapper.vm.$nextTick()
    expect(wrapper.get('.date-picker-trigger').text()).toContain('All time')
    expect(wrapper.emitted('change')).toBeUndefined()
    await wrapper.get('.date-picker-trigger').trigger('click')
    expect((wrapper.findAll('input')[0].element as HTMLInputElement).value).toBe('')
    wrapper.unmount()
  })

  it('positions the datetime popup above a low trigger and inside the viewport', async () => {
    const wrapper = mount(DateRangePicker, { props: { startDate: '', endDate: '', includeTime: true } })
    const trigger = wrapper.get('.date-picker-trigger').element
    vi.spyOn(trigger, 'getBoundingClientRect').mockReturnValue({ top: 650, bottom: 690, left: 30, right: 290, width: 260, height: 40, x: 30, y: 650, toJSON: () => ({}) })
    await wrapper.get('.date-picker-trigger').trigger('click')
    const popup = wrapper.get('.date-picker-dropdown').element as HTMLElement
    expect(popup.style.position).toBe('fixed')
    expect(parseFloat(popup.style.bottom)).toBeGreaterThan(0)
    expect(parseFloat(popup.style.left)).toBeGreaterThanOrEqual(16)
    expect(parseFloat(popup.style.left) + parseFloat(popup.style.width)).toBeLessThanOrEqual(window.innerWidth - 16)
    wrapper.unmount()
  })
})
