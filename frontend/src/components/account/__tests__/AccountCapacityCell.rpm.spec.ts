import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import AccountCapacityCell from '../AccountCapacityCell.vue'
import CapacityBadge from '../CapacityBadge.vue'
import type { Account } from '@/types'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

const account = {
  id: 1,
  platform: 'anthropic',
  type: 'oauth',
  concurrency: 10,
  base_rpm: 100,
  rpm_strategy: 'tiered',
  rpm_sticky_buffer: 20,
  current_rpm: 150,
  scheduling_rpm: 20
} as Account

function rpmBadge(wrapper: ReturnType<typeof mount>) {
  return wrapper.findAllComponents(CapacityBadge).find(badge => badge.props('suffix'))!
}

describe('AccountCapacityCell scheduling RPM', () => {
  it('uses calendar-minute scheduling counts for capacity and tiered thresholds', async () => {
    const wrapper = mount(AccountCapacityCell, { props: { account } })
    const badge = rpmBadge(wrapper)
    expect(badge.props('current')).toBe(20)
    expect(badge.props('colorClass')).toContain('emerald')
    expect(badge.props('tooltip')).toContain('admin.accounts.capacity.rpm.calendarMinute')
    expect(badge.props('tooltip')).toContain('admin.accounts.capacity.rpm.tieredNormal')

    for (const [count, color, state] of [
      [80, 'yellow', 'tieredWarning'],
      [100, 'orange', 'tieredStickyOnly'],
      [120, 'red', 'tieredBlocked'],
      [0, 'emerald', 'tieredNormal']
    ] as const) {
      await wrapper.setProps({ account: { ...account, scheduling_rpm: count } })
      expect(badge.props('current')).toBe(count)
      expect(badge.props('colorClass')).toContain(color)
      expect(badge.props('tooltip')).toContain(`admin.accounts.capacity.rpm.${state}`)
    }
    wrapper.unmount()
  })

  it('keeps sticky exemptions based on scheduling counts when rolling RPM is lower', () => {
    const wrapper = mount(AccountCapacityCell, {
      props: { account: { ...account, rpm_strategy: 'sticky_exempt', current_rpm: 1, scheduling_rpm: 130 } }
    })
    const badge = rpmBadge(wrapper)
    expect(badge.props('current')).toBe(130)
    expect(badge.props('colorClass')).toContain('orange')
    expect(badge.props('tooltip')).toContain('admin.accounts.capacity.rpm.stickyExemptOver')
    wrapper.unmount()
  })

  it.each([undefined, null])('shows an unavailable neutral limit when scheduling RPM is %s', schedulingRpm => {
    const wrapper = mount(AccountCapacityCell, {
      props: { account: { ...account, scheduling_rpm: schedulingRpm } }
    })
    const badge = rpmBadge(wrapper)
    expect(badge.props('current')).toBe('common.notAvailable')
    expect(badge.props('colorClass')).toContain('bg-gray-100')
    expect(badge.props('tooltip')).toContain('common.notAvailable')
    expect(badge.text()).not.toContain('150')
    wrapper.unmount()
  })
})
