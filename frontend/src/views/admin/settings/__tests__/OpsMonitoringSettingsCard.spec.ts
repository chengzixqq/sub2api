import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { defineComponent, h } from 'vue'

import OpsMonitoringSettingsCard from '../OpsMonitoringSettingsCard.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

const SelectStub = defineComponent({
  props: {
    modelValue: { type: [String, Number, Boolean, null], default: '' },
    options: { type: Array, default: () => [] },
  },
  emits: ['update:modelValue'],
  setup(props, { emit }) {
    return () =>
      h(
        'select',
        {
          value: props.modelValue ?? '',
          onChange: (event: Event) => emit('update:modelValue', (event.target as HTMLSelectElement).value),
        },
        (props.options as Array<{ value: string; label: string }>).map((option) =>
          h('option', { value: option.value }, option.label),
        ),
      )
  },
})

function mountCard(props: Partial<InstanceType<typeof OpsMonitoringSettingsCard>['$props']> = {}) {
  return mount(OpsMonitoringSettingsCard, {
    props: {
      enabled: true,
      realtimeEnabled: true,
      queryMode: 'auto',
      metricsIntervalSeconds: 60,
      configEnabled: true,
      ...props,
    },
    global: { stubs: { Select: SelectStub } },
  })
}

describe('OpsMonitoringSettingsCard', () => {
  it('shows realtime, query mode and interval only while monitoring is enabled', async () => {
    const wrapper = mountCard()
    expect(wrapper.text()).toContain('admin.settings.opsMonitoring.title')
    expect(wrapper.find('[data-testid="ops-realtime-monitoring-toggle"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="ops-query-mode-select"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="ops-metrics-interval-input"]').exists()).toBe(true)

    await wrapper.setProps({ enabled: false })
    expect(wrapper.find('[data-testid="ops-monitoring-enabled-toggle"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="ops-realtime-monitoring-toggle"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="ops-query-mode-select"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="ops-metrics-interval-input"]').exists()).toBe(false)
  })

  it('emits v-model updates for every control', async () => {
    const wrapper = mountCard()
    await wrapper.get('[data-testid="ops-monitoring-enabled-toggle"]').trigger('click')
    expect(wrapper.emitted('update:enabled')).toEqual([[false]])

    await wrapper.get('[data-testid="ops-realtime-monitoring-toggle"]').trigger('click')
    expect(wrapper.emitted('update:realtimeEnabled')).toEqual([[false]])

    await wrapper.get('[data-testid="ops-query-mode-select"] select').setValue('raw')
    expect(wrapper.emitted('update:queryMode')).toEqual([['raw']])

    await wrapper.get('[data-testid="ops-metrics-interval-input"]').setValue('120')
    expect(wrapper.emitted('update:metricsIntervalSeconds')?.at(-1)).toEqual([120])
  })

  it('locks the switch and explains when the server config disables ops', async () => {
    const wrapper = mountCard({ enabled: true, configEnabled: false })
    const toggle = wrapper.get('[data-testid="ops-monitoring-enabled-toggle"]')
    expect(toggle.attributes('disabled')).toBeDefined()
    expect(toggle.attributes('aria-checked')).toBe('false')
    expect(wrapper.get('[data-testid="ops-monitoring-config-disabled-hint"]').text()).toBe(
      'admin.settings.opsMonitoring.configDisabledHint',
    )
    expect(wrapper.find('[data-testid="ops-realtime-monitoring-toggle"]').exists()).toBe(false)

    await toggle.trigger('click')
    expect(wrapper.emitted('update:enabled')).toBeUndefined()
  })

  it('treats an unknown config state as enabled', () => {
    const wrapper = mountCard({ configEnabled: null })
    const toggle = wrapper.get('[data-testid="ops-monitoring-enabled-toggle"]')
    expect(toggle.attributes('disabled')).toBeUndefined()
    expect(wrapper.find('[data-testid="ops-monitoring-config-disabled-hint"]').exists()).toBe(false)
  })
})
