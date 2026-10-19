import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, shallowMount } from '@vue/test-utils'
import { ref } from 'vue'

vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key }),
}))
enableAutoUnmount(afterEach)

import OpsAlertRulesCard from '../OpsAlertRulesCard.vue'
import Select from '@/components/common/Select.vue'

const { presetP99Rule, updateAlertRule } = vi.hoisted(() => ({
  presetP99Rule: {
    id: 3,
    name: 'P99延迟过高',
    description: '',
    enabled: true,
    metric_type: 'p99_latency_ms',
    operator: '>',
    threshold: 3000,
    window_minutes: 5,
    sustained_minutes: 10,
    severity: 'P2',
    cooldown_minutes: 30,
    notify_email: true,
  },
  updateAlertRule: vi.fn(),
}))
vi.mock('@vueuse/core', () => ({ useMediaQuery: () => ref(true) }))
vi.mock('@/api', () => ({ adminAPI: { groups: { getAll: vi.fn().mockResolvedValue([]) } } }))
vi.mock('@/api/admin/ops', () => ({
  opsAPI: { listAlertRules: vi.fn().mockResolvedValue([presetP99Rule]), createAlertRule: vi.fn(), updateAlertRule },
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() }) }))

async function openPresetRule() {
  const wrapper = shallowMount(OpsAlertRulesCard, {
    global: { stubs: { BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' } } },
  })
  await flushPromises()
  await wrapper.findAll('button').find(b => b.text() === 'common.edit')!.trigger('click')
  return wrapper
}

describe('alert rule latency metrics', () => {
  beforeEach(() => { vi.clearAllMocks() })

  it('offers P95 / P99 latency in the metric picker and describes the preset rule', async () => {
    const wrapper = await openPresetRule()
    const metricSelect = wrapper.findAllComponents(Select)[0]
    const options = metricSelect.props('options') as Array<{ value: unknown, label: string }>
    expect(options).toEqual(expect.arrayContaining([
      expect.objectContaining({ value: 'p95_latency_ms', label: 'admin.ops.alertRules.metrics.p95' }),
      expect.objectContaining({ value: 'p99_latency_ms', label: 'admin.ops.alertRules.metrics.p99' }),
    ]))
    expect(metricSelect.props('modelValue')).toBe('p99_latency_ms')
    expect(wrapper.text()).toContain('admin.ops.alertRules.metricDescriptions.p99')
  })

  it('saves the edited preset rule with its latency metric', async () => {
    const wrapper = await openPresetRule()
    await wrapper.findAll('button').find(b => b.text() === 'common.save')!.trigger('click')
    await flushPromises()
    expect(updateAlertRule).toHaveBeenCalledWith(3, expect.objectContaining({
      metric_type: 'p99_latency_ms',
      threshold: 3000,
      window_minutes: 5,
    }))
  })
})
