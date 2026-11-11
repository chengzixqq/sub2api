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

  it('displays new cache buckets and renders historical missing buckets as zero', () => {
    const ttlDetails = { ...details, components: [{ ...details.components[0]!, cache_creation_30m_tokens: 11, cache_creation_12h_tokens: 19 }] }
    const wrapper = mount(UsageBillingDetails, { props: { details: ttlDetails, showAccountBilling: false } })
    const bucket = (label: string) => wrapper.findAll('dt').find(item => item.text() === label)!.element.nextElementSibling!.textContent
    expect(bucket('usage.billingCache30m')).toBe('11')
    expect(bucket('usage.billingCache12h')).toBe('19')
    wrapper.unmount()
    const historical = mount(UsageBillingDetails, { props: { details, showAccountBilling: false } })
    for (const label of ['usage.billingCache30m', 'usage.billingCache12h']) {
      expect(historical.findAll('dt').find(item => item.text() === label)!.element.nextElementSibling!.textContent).toBe('0')
    }
  })
})
