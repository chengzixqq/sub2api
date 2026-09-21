import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import CustomizationView from '../CustomizationView.vue'
import Select from '@/components/common/Select.vue'
import Toggle from '@/components/common/Toggle.vue'

const { getSettings, applyPreset, updateSettings, showError } = vi.hoisted(() => ({
  getSettings: vi.fn(),
  applyPreset: vi.fn(),
  updateSettings: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/api/admin/settings', () => ({
  default: {},
  getClaudeCustomization: getSettings,
  applyClaudeCustomizationPreset: applyPreset,
  updateClaudeCustomization: updateSettings,
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError }) }))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key }),
}))

const policy = {
  preset: 'custom',
  fallback_policy: 'native_passthrough',
  thinking_prefilter_enabled: false,
  thinking_signature_retry_enabled: false,
  thinking_tool_downgrade_retry_enabled: false,
  beta_policy_mode: 'capability_aware',
  unknown_beta_action: 'pass_on_native_only',
  fingerprint_unification: true,
  metadata_passthrough: true,
  url_redaction_enabled: true,
}

function renderView() {
  return mount(CustomizationView, {
    global: { stubs: { AppLayout: { template: '<main><slot /></main>' }, Icon: true } },
  })
}

describe('CustomizationView native settings UI', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getSettings.mockResolvedValue({ global: { ...policy } })
    updateSettings.mockImplementation(async (value) => ({ global: { ...value } }))
    applyPreset.mockResolvedValue({ global: { ...policy, preset: 'official', fallback_policy: 'strict' } })
  })

  it('uses shared selects and switches with descriptions instead of raw controls', async () => {
    const wrapper = renderView()
    await flushPromises()
    expect(wrapper.findAllComponents(Select)).toHaveLength(4)
    expect(wrapper.findAllComponents(Toggle)).toHaveLength(6)
    expect(wrapper.find('input[type="checkbox"]').exists()).toBe(false)
    expect(wrapper.find('select').exists()).toBe(false)
    for (const toggle of wrapper.findAllComponents(Toggle)) {
      const id = toggle.attributes('aria-describedby')
      expect(id).toBeTruthy()
      expect(wrapper.get(`#${id}`).text()).toContain('.hint')
      expect(toggle.attributes('aria-label')).toContain('.label')
    }
    wrapper.unmount()
  })

  it('keeps editable defaults hidden until the initial settings request finishes', async () => {
    let finish!: (value: unknown) => void
    getSettings.mockReturnValue(new Promise((resolve) => { finish = resolve }))
    const wrapper = renderView()
    expect(wrapper.findAllComponents(Toggle)).toHaveLength(0)
    expect(wrapper.find('form').exists()).toBe(false)
    finish({ global: { ...policy } })
    await flushPromises()
    expect(wrapper.find('form').exists()).toBe(true)
    wrapper.unmount()
  })

  it('offers retry without allowing defaults to be saved after a load failure', async () => {
    getSettings.mockRejectedValueOnce(new Error('load failed'))
    const wrapper = renderView()
    await flushPromises()
    expect(wrapper.find('form').exists()).toBe(false)
    expect(showError).toHaveBeenCalledOnce()
    await wrapper.get('[data-testid="customization-retry"]').trigger('click')
    await flushPromises()
    expect(getSettings).toHaveBeenCalledTimes(2)
    expect(wrapper.find('form').exists()).toBe(true)
    expect(updateSettings).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('preserves the sparse policy values when switching a boolean and saving', async () => {
    const wrapper = renderView()
    await flushPromises()
    const toggle = wrapper.findAllComponents(Toggle).find((item) => item.attributes('aria-label').includes('url_redaction_enabled'))!
    await toggle.trigger('click')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(updateSettings).toHaveBeenCalledWith({ ...policy, url_redaction_enabled: false, preset: 'custom' })
    expect(wrapper.get('[role="status"]').text()).toContain('common.saved')
    wrapper.unmount()
  })

  it('applies presets through the existing endpoint and locks competing changes', async () => {
    let finish!: (value: unknown) => void
    applyPreset.mockReturnValue(new Promise((resolve) => { finish = resolve }))
    const wrapper = renderView()
    await flushPromises()
    wrapper.getComponent(Select).vm.$emit('update:modelValue', 'official')
    await flushPromises()
    expect(applyPreset).toHaveBeenCalledWith('official')
    expect(wrapper.findAllComponents(Toggle).every((item) => item.props('disabled'))).toBe(true)
    finish({ global: { ...policy, preset: 'official', fallback_policy: 'strict' } })
    await flushPromises()
    expect(wrapper.getComponent(Select).props('modelValue')).toBe('official')
    expect(wrapper.findAllComponents(Toggle).every((item) => !item.props('disabled'))).toBe(true)
    wrapper.unmount()
  })

  it('preserves edits and unlocks controls when saving fails', async () => {
    updateSettings.mockRejectedValueOnce(new Error('save failed'))
    const wrapper = renderView()
    await flushPromises()
    const toggle = wrapper.findAllComponents(Toggle).find((item) => item.attributes('aria-label').includes('url_redaction_enabled'))!
    await toggle.trigger('click')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(showError).toHaveBeenCalledOnce()
    expect(toggle.props('modelValue')).toBe(false)
    expect(toggle.props('disabled')).toBe(false)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(updateSettings).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })
})
