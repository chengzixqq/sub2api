import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import AccountCapacityCell from '../AccountCapacityCell.vue'
import type { Account } from '@/types'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

const makeAccount = (overrides: Partial<Account> = {}): Account => ({
  id: 1,
  name: 'probe-account',
  platform: 'openai',
  type: 'apikey',
  proxy_id: null,
  concurrency: 10,
  current_concurrency: 4,
  probe_concurrency: 3,
  priority: 1,
  status: 'active',
  error_message: null,
  last_used_at: null,
  expires_at: null,
  auto_pause_on_expired: true,
  created_at: '2026-08-23T00:00:00Z',
  updated_at: '2026-08-23T00:00:00Z',
  schedulable: true,
  rate_limit_reset_at: null,
  rate_limited_at: null,
  overload_until: null,
  temp_unschedulable_until: null,
  temp_unschedulable_reason: null,
  session_window_start: null,
  session_window_end: null,
  session_window_status: null,
  ...overrides
})

const globalStubs = {
  CapacityBadge: {
    props: ['colorClass', 'tooltip', 'current', 'max', 'suffix'],
    template: '<span data-test="capacity-badge" :title="tooltip">{{ current }}/{{ max }}</span>'
  },
  QuotaBadge: true
}

describe('AccountCapacityCell probe concurrency', () => {
  it('keeps real concurrency and probe concurrency on separate lines', () => {
    const wrapper = mount(AccountCapacityCell, {
      props: { account: makeAccount() },
      global: { stubs: globalStubs }
    })

    expect(wrapper.get('[data-test="capacity-badge"]').text()).toBe('4/10')
    const probe = wrapper.get('[data-test="probe-concurrency"]')
    expect(probe.text()).toContain('admin.accounts.capacity.probeShort')
    expect(probe.text()).toContain('3')
    expect(probe.element.previousElementSibling?.getAttribute('data-test')).toBe('capacity-badge')
  })

  it('keeps the probe row visible when no probe leader is active', () => {
    const wrapper = mount(AccountCapacityCell, {
      props: { account: makeAccount({ probe_concurrency: 0 }) },
      global: { stubs: globalStubs }
    })

    expect(wrapper.get('[data-test="probe-concurrency"]').text()).toContain('0')
  })
})
