import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import UsageBillingDetails from '../UsageBillingDetails.vue'
import type { AdminUsageBillingDetails } from '@/types'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const details: AdminUsageBillingDetails = {
  version: 1,
  status: 'partial_unpriced',
  components: [{
    role: 'advisor', iteration_index: 0, model: 'claude-opus-advisor', billing_mode: 'token',
    input_tokens: 20, output_tokens: 10, cache_creation_tokens: 7, cache_read_tokens: 5,
    cache_creation_5m_tokens: 3, cache_creation_1h_tokens: 4,
    input_cost: null, output_cost: null, cache_creation_cost: null, cache_read_cost: null,
    total_cost: null, actual_cost: null, account_stats_cost: 123.4567, pricing_source: 'private-rule',
    unpriced_reason: 'advisor_pricing_unavailable',
  }],
}

describe('UsageBillingDetails', () => {
  it('shows advisor model, partial status and unpriced values without admin metadata', () => {
    const wrapper = mount(UsageBillingDetails, { props: { details, showAccountBilling: false } })
    expect(wrapper.text()).toContain('claude-opus-advisor')
    expect(wrapper.text()).toContain('usage.billingRole.advisor')
    expect(wrapper.text()).toContain('usage.billingStatus.partial_unpriced')
    expect(wrapper.text()).toContain('usage.billingNotPriced')
    expect(wrapper.text()).toContain('usage.billingDetailsNote')
    expect(wrapper.text()).not.toContain('$0.00000000')
    expect(wrapper.text()).not.toContain('123.4567')
    expect(wrapper.text()).not.toContain('private-rule')
    expect(wrapper.text()).not.toContain('usage.billingAccountStatsCost')
  })

  it('shows explicit account base cost and pricing source for admins', () => {
    const wrapper = mount(UsageBillingDetails, { props: { details, showAccountBilling: true } })
    expect(wrapper.text()).toContain('$123.45670000')
    expect(wrapper.text()).toContain('private-rule')
    expect(wrapper.text()).toContain('usage.billingAccountStatsCost')
  })
})
