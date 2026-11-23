import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import CustomizationView from '../CustomizationView.vue'
import Select from '@/components/common/Select.vue'
import Toggle from '@/components/common/Toggle.vue'

import { alignmentDefaults, alignmentFields, schema } from '@/components/admin/customization/__tests__/alignmentSchema'

const { getSchema, getSettings, applyPreset, updateSettings, resetField, showError } = vi.hoisted(() => ({
  resetField: vi.fn(),
  getSchema: vi.fn(),
  getSettings: vi.fn(),
  applyPreset: vi.fn(),
  updateSettings: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/api/admin/settings', () => ({
  default: {},
  getClaudeCustomizationSchema: getSchema,
  getClaudeCustomization: getSettings,
  resetClaudeCustomizationField: resetField,
  resetClaudeCustomization: resetField,
  applyClaudeCustomizationPreset: applyPreset,
  updateClaudeCustomization: updateSettings,
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError }) }))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key, te: () => false, tm: () => [], rt: (value: string) => value }),
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
  ...alignmentDefaults,
}

const unchangedAlignment = {
  context_management_missing_beta: '', request_id_mode: '', cache_control_limit_mode: '',
  unknown_beta_header: '', models_catalog: '', upstream_header_passthrough: '', thinking_cache_control: '',
  bedrock_cc_context_management: '',
}

function renderView() {
  return mount(CustomizationView, {
    global: { stubs: { AppLayout: { template: '<main><slot /></main>' }, Icon: true } },
  })
}

describe('CustomizationView native settings UI', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getSchema.mockResolvedValue(schema)
    getSettings.mockResolvedValue({ global: { ...policy } })
    updateSettings.mockImplementation(async (value) => ({
      global: { ...policy, ...Object.fromEntries(Object.entries(value).filter(([, entry]) => entry !== '')) },
    }))
    applyPreset.mockResolvedValue({ global: { ...policy, preset: 'official', fallback_policy: 'strict' } })
  })

  it('recovers the whole corrupt document through the reset endpoint',async()=>{
    getSettings.mockRejectedValueOnce({reason:'CLAUDE_CUSTOMIZATION_CORRUPT'})
    const wrapper=renderView()
    await flushPromises()
    await wrapper.get('[data-reset-all]').trigger('click')
    await flushPromises()
    expect(resetField).toHaveBeenCalledWith()
    expect(getSettings).toHaveBeenCalledTimes(2)
  })

  it('repairs only the selected invalid field', async () => {
    getSettings.mockResolvedValueOnce({ global: policy, invalid_fields: ['request_id_mode', 'preset'] })
    resetField.mockResolvedValueOnce({ global: policy })
    getSettings.mockResolvedValueOnce({ global: policy, invalid_fields: ['preset'] })
    const wrapper = renderView()
    await flushPromises()
    await wrapper.get('[data-reset-field="request_id_mode"]').trigger('click')
    await flushPromises()
    expect(resetField).toHaveBeenCalledWith('request_id_mode')
    expect(getSettings).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[data-reset-field="preset"]').exists()).toBe(true)
    expect(wrapper.find('[data-reset-field="request_id_mode"]').exists()).toBe(false)
    expect(updateSettings).not.toHaveBeenCalled()
  })

  it('uses shared selects and switches with descriptions instead of raw controls', async () => {
    const wrapper = renderView()
    await flushPromises()
    expect(wrapper.findAllComponents(Select)).toHaveLength(4 + alignmentFields.length)
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
    expect(updateSettings).toHaveBeenCalledWith({ ...policy, ...unchangedAlignment, url_redaction_enabled: false, preset: 'custom' })
    expect(wrapper.get('[role="status"]').text()).toContain('common.saved')
    wrapper.unmount()
  })

  it('switches from magic to custom when enabling signature retries', async () => {
    getSettings.mockResolvedValueOnce({ global: { ...policy, preset: 'magic' } })
    const wrapper = renderView()
    await flushPromises()
    const toggle = wrapper.findAllComponents(Toggle).find((item) => item.attributes('aria-label').includes('thinking_signature_retry_enabled'))!
    await toggle.trigger('click')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(updateSettings).toHaveBeenCalledWith({ ...policy, ...unchangedAlignment, preset: 'custom', thinking_signature_retry_enabled: true })
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

  it('loads the schema alongside settings and moves only alignment fields into their section', async () => {
    let finish!: (value: unknown) => void
    getSchema.mockReturnValueOnce(new Promise(resolve => { finish = resolve }))
    const wrapper = renderView()
    expect(getSchema).toHaveBeenCalledOnce()
    expect(getSettings).toHaveBeenCalledOnce()
    expect(wrapper.find('form').exists()).toBe(false)
    finish(schema)
    await flushPromises()
    expect(wrapper.findAll('[data-field]').map(row => row.attributes('data-field'))).toEqual(alignmentFields.map(f => f.key))
    const compatibility = wrapper.get('section[aria-labelledby="customization-compatibility"]')
    expect(compatibility.text()).not.toContain('cache_control_limit_mode')
    wrapper.unmount()
  })

  it('sends only the changed alignment value without changing the preset or other fields', async () => {
    const global = { ...policy, preset: 'magic', thinking_cache_control: 'passthrough' }
    getSettings.mockResolvedValueOnce({ global })
    const wrapper = renderView()
    await flushPromises()
    const select = wrapper.get('[data-field="cache_control_limit_mode"]').getComponent(Select)
    select.vm.$emit('update:modelValue', 'reject')
    await flushPromises()
    expect(wrapper.getComponent(Select).props('modelValue')).toBe('magic')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(updateSettings).toHaveBeenCalledWith({ ...global, ...unchangedAlignment, cache_control_limit_mode: 'reject' })
    wrapper.unmount()
  })


  it('sends empty strings for all seven alignment fields when none was edited', async () => {
    const wrapper = renderView()
    await flushPromises()
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(updateSettings).toHaveBeenCalledWith({ ...policy, ...unchangedAlignment })
    wrapper.unmount()
  })

  it.each(alignmentFields)('displays the schema default when $key is absent', async (field) => {
    const global: Record<string, unknown> = { ...policy }
    delete global[field.key]
    getSettings.mockResolvedValueOnce({ global })
    const wrapper = renderView()
    await flushPromises()
    expect(wrapper.get(`[data-field="${field.key}"]`).getComponent(Select).props('modelValue')).toBe(field.default)
    wrapper.unmount()
  })

  it.each([undefined, null, false])('does not serialize a non-string sibling (%s) when another field changes', async (value) => {
    getSettings.mockResolvedValueOnce({ global: { ...policy, request_id_mode: value } })
    const wrapper = renderView()
    await flushPromises()
    expect(wrapper.get('[data-field="request_id_mode"]').getComponent(Select).props('modelValue')).toBe('passthrough')
    wrapper.get('[data-field="cache_control_limit_mode"]').getComponent(Select).vm.$emit('update:modelValue', 'reject')
    await flushPromises()
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(updateSettings).toHaveBeenCalledWith({ ...policy, ...unchangedAlignment, cache_control_limit_mode: 'reject' })
    expect(wrapper.get('[data-field="request_id_mode"]').getComponent(Select).props('modelValue')).toBe('passthrough')
    wrapper.unmount()
  })

  it('uses the schema default rather than a hardcoded default for an absent field', async () => {
    getSchema.mockResolvedValueOnce(schema.map(field => field.key === 'request_id_mode' ? { ...field, default: 'fill' } : field))
    const global: Record<string, unknown> = { ...policy }
    delete global.request_id_mode
    getSettings.mockResolvedValueOnce({ global })
    const wrapper = renderView()
    await flushPromises()
    expect(wrapper.get('[data-field="request_id_mode"]').getComponent(Select).props('modelValue')).toBe('fill')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(updateSettings).toHaveBeenCalledWith({ ...policy, ...unchangedAlignment })
    wrapper.unmount()
  })

  it('refreshes server values and clears edited alignment fields after a successful save', async () => {
    updateSettings.mockResolvedValueOnce({ global: { ...policy, request_id_mode: 'fill', unknown_beta_header: 'reject' } })
    const wrapper = renderView()
    await flushPromises()
    wrapper.get('[data-field="request_id_mode"]').getComponent(Select).vm.$emit('update:modelValue', 'fill')
    await flushPromises()
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(updateSettings).toHaveBeenNthCalledWith(1, { ...policy, ...unchangedAlignment, request_id_mode: 'fill' })
    expect(wrapper.get('[data-field="unknown_beta_header"]').getComponent(Select).props('modelValue')).toBe('reject')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(updateSettings).toHaveBeenNthCalledWith(2, { ...policy, ...unchangedAlignment })
    wrapper.unmount()
  })

  it('keeps edited alignment fields for a retry after a failed save', async () => {
    updateSettings.mockRejectedValueOnce(new Error('save failed'))
    const wrapper = renderView()
    await flushPromises()
    wrapper.get('[data-field="request_id_mode"]').getComponent(Select).vm.$emit('update:modelValue', 'fill')
    await flushPromises()
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(updateSettings).toHaveBeenNthCalledWith(2, { ...policy, ...unchangedAlignment, request_id_mode: 'fill' })
    wrapper.unmount()
  })

  it('clears unsaved alignment edits after applying a preset', async () => {
    const wrapper = renderView()
    await flushPromises()
    wrapper.get('[data-field="request_id_mode"]').getComponent(Select).vm.$emit('update:modelValue', 'fill')
    await flushPromises()
    wrapper.getComponent(Select).vm.$emit('update:modelValue', 'official')
    await flushPromises()
    expect(wrapper.get('[data-field="request_id_mode"]').getComponent(Select).props('modelValue')).toBe('passthrough')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(updateSettings).toHaveBeenCalledWith({ ...policy, ...unchangedAlignment, preset: 'official', fallback_policy: 'strict' })
    wrapper.unmount()
  })

  it('keeps all seven persisted alignment values when applying a preset', async () => {
    const global = { ...policy, cache_control_limit_mode: 'passthrough', request_id_mode: 'fill' }
    getSettings.mockResolvedValueOnce({ global })
    applyPreset.mockResolvedValueOnce({ global: { ...global, preset: 'official', fallback_policy: 'strict' } })
    const wrapper = renderView()
    await flushPromises()
    wrapper.getComponent(Select).vm.$emit('update:modelValue', 'official')
    await flushPromises()
    expect(wrapper.getComponent(Select).props('modelValue')).toBe('official')
    for (const [key, value] of Object.entries({ ...alignmentDefaults, cache_control_limit_mode: 'passthrough', request_id_mode: 'fill' })) {
      expect(wrapper.get(`[data-field="${key}"]`).getComponent(Select).props('modelValue')).toBe(value)
    }
    wrapper.unmount()
  })
})
