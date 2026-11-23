import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, shallowMount, type VueWrapper } from '@vue/test-utils'
import type { Account } from '@/types'

import Select from '@/components/common/Select.vue'
import { alignmentDefaults, alignmentFields, schema } from '@/components/admin/customization/__tests__/alignmentSchema'

const { getSchema, getCustomization, updateCustomization, patchAccount } = vi.hoisted(() => ({
  getSchema: vi.fn(), getCustomization: vi.fn(), updateCustomization: vi.fn(), patchAccount: vi.fn()
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn() }) }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ isOwner: true, isVendor: false, isSimpleMode: true }) }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string) => key, te: () => false, tm: () => [], rt: (value: string) => value }) }))
vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: { getClaudeCustomization: getCustomization, updateClaudeCustomization: updateCustomization, patch: patchAccount, checkMixedChannelRisk: vi.fn().mockResolvedValue({ has_risk: false }) },
    settings: { getClaudeCustomizationSchema: getSchema, getWebSearchEmulationConfig: vi.fn().mockResolvedValue({ enabled: false, providers: [] }), getSettings: vi.fn().mockResolvedValue({}) },
    tlsFingerprintProfiles: { list: vi.fn().mockResolvedValue([]) }
  }
}))
vi.mock('@/api/admin/accounts', () => ({ getAntigravityDefaultModelMapping: vi.fn() }))
import EditAccountModal from '../EditAccountModal.vue'

function account(id: number): Account {
  return { id, name: `Claude ${id}`, platform: 'anthropic', type: 'apikey', credentials: { api_key: 'test-fixture', base_url: 'https://example.invalid' }, extra: {}, proxy_id: null, concurrency: 1, priority: 1, rate_multiplier: 1, status: 'active', group_ids: [] } as unknown as Account
}
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(done => { resolve = done })
  return { promise, resolve }
}
const wrappers: VueWrapper[] = []
function mountModal(id = 1, platform: Account['platform'] = 'anthropic', type: Account['type'] = 'apikey') {
  const wrapper = shallowMount(EditAccountModal, {
    props: { show: true, account: { ...account(id), platform, type }, accountEtag: `etag-${id}`, proxies: [], groups: [] },
    global: { stubs: { OfficialAlignmentCard: false, BaseDialog: defineComponent({ template: '<div><slot /><slot name="footer" /></div>' }) } }
  })
  wrappers.push(wrapper)
  return wrapper
}
function fallback(wrapper: VueWrapper) { return wrapper.findAll('select')[0] }
async function saveName(wrapper: VueWrapper) {
  await wrapper.get('[data-tour="edit-account-form-name"]').setValue('Renamed')
  await wrapper.get('#edit-account-form').trigger('submit')
  await flushPromises()
}

describe('EditAccountModal Claude policy isolation', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getSchema.mockReset().mockResolvedValue(schema)
    getCustomization.mockReset().mockResolvedValue({ overrides: {} })
    updateCustomization.mockResolvedValue({ overrides: {} })
    patchAccount.mockImplementation(async (id: number) => account(id))
  })
  afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()) })

  it('does not write the previous account policy when the next response omits overrides', async () => {
    getCustomization.mockResolvedValueOnce({ overrides: { fallback_policy: 'strict' } }).mockResolvedValueOnce({})
    const wrapper = mountModal()
    await flushPromises()
    expect((fallback(wrapper).element as HTMLSelectElement).value).toBe('strict')
    await wrapper.setProps({ account: account(2), accountEtag: 'etag-2' })
    await flushPromises()
    expect((fallback(wrapper).element as HTMLSelectElement).value).not.toBe('strict')
    await saveName(wrapper)
    expect(patchAccount).toHaveBeenCalledWith(2, expect.objectContaining({ name: 'Renamed' }), 'etag-2')
    expect(updateCustomization).not.toHaveBeenCalled()
  })

  it('ignores late policy responses from another account', async () => {
    const first = deferred<{ overrides: Record<string, unknown> }>()
    getCustomization.mockReturnValueOnce(first.promise).mockResolvedValueOnce({ overrides: { fallback_policy: 'native_passthrough' } })
    const wrapper = mountModal()
    await wrapper.setProps({ account: account(2), accountEtag: 'etag-2' })
    await flushPromises()
    first.resolve({ overrides: { fallback_policy: 'strict' } })
    await flushPromises()
    expect((fallback(wrapper).element as HTMLSelectElement).value).toBe('native_passthrough')
    await saveName(wrapper)
    expect(updateCustomization).not.toHaveBeenCalled()
  })

  it('keeps the main account save usable when policy loading fails', async () => {
    getCustomization.mockRejectedValueOnce(new Error('unavailable'))
    const wrapper = mountModal()
    await flushPromises()
    await saveName(wrapper)
    expect(patchAccount).toHaveBeenCalledTimes(1)
    expect(updateCustomization).not.toHaveBeenCalled()
    expect(wrapper.emitted('updated')).toHaveLength(1)
  })

  it('does not clear server policies when saving before policy loading completes', async () => {
    const pending = deferred<{ overrides: Record<string, unknown> }>()
    getCustomization.mockReturnValueOnce(pending.promise)
    const wrapper = mountModal()
    await saveName(wrapper)
    expect(patchAccount).toHaveBeenCalledTimes(1)
    expect(updateCustomization).not.toHaveBeenCalled()
    pending.resolve({ overrides: { fallback_policy: 'strict' } })
    await flushPromises()
  })

  it('persists an explicit edit and freezes it before the account save awaits', async () => {
    getCustomization.mockResolvedValueOnce({ overrides: { fallback_policy: 'strict' } }).mockResolvedValueOnce({ overrides: { fallback_policy: 'fable_native_passthrough' } })
    const pendingSave = deferred<Account>()
    patchAccount.mockReturnValueOnce(pendingSave.promise)
    const wrapper = mountModal()
    await flushPromises()
    await fallback(wrapper).setValue('native_passthrough')
    await wrapper.get('#edit-account-form').trigger('submit')
    await wrapper.setProps({ account: account(2), accountEtag: 'etag-2' })
    await flushPromises()
    pendingSave.resolve(account(1))
    await flushPromises()
    expect(updateCustomization).toHaveBeenCalledWith(1, { fallback_policy: 'native_passthrough' })
  })

  it('retries the override using the main edit version after a partial save',async()=>{
    getCustomization.mockResolvedValueOnce({overrides:{fallback_policy:'strict'}})
    updateCustomization.mockRejectedValueOnce(new Error('Transient override failure'))
    patchAccount.mockResolvedValueOnce({...account(1),config_version:8})
    const wrapper=mountModal()
    await flushPromises()
    await fallback(wrapper).setValue('native_passthrough')
    await saveName(wrapper)
    await saveName(wrapper)
    expect(patchAccount.mock.calls.at(-1)?.[2]).toBe('"cv-8"')
  })

  it('restores inheritance with an explicit null enum', async () => {
    getCustomization.mockResolvedValueOnce({ overrides: { fallback_policy: 'strict' } })
    const wrapper = mountModal()
    await flushPromises()
    await fallback(wrapper).setValue('')
    await saveName(wrapper)
    expect(updateCustomization).toHaveBeenCalledWith(1, { fallback_policy: null })
  })

  it('uses the card for sparse alignment overrides and preserves the other settings', async () => {
    getCustomization.mockResolvedValueOnce({
      overrides: { fallback_policy: 'strict', metadata_passthrough: false },
      global: alignmentDefaults, hits: { unknown_beta_header: { last_24h: 3, last_7d: 10 } },
    })
    const wrapper = mountModal()
    await flushPromises()
    expect(getSchema).toHaveBeenCalledOnce()
    expect(wrapper.find('[data-testid="claude-override-cache-control-limit-mode"]').exists()).toBe(false)
    expect(wrapper.findAll('[data-field]')).toHaveLength(8)
    const card = wrapper.getComponent({ name: 'OfficialAlignmentCard' })
    expect(card.props('globalValues')).toMatchObject(alignmentDefaults)
    expect(card.props('hits')).toEqual({ unknown_beta_header: { last_24h: 3, last_7d: 10 } })
    const select = wrapper.get('[data-field="cache_control_limit_mode"]').getComponent(Select)
    expect(select.props('modelValue')).toBeNull()
    select.vm.$emit('update:modelValue', 'reject')
    await saveName(wrapper)
    expect(updateCustomization).toHaveBeenCalledWith(1, {
      cache_control_limit_mode: 'reject',
    })
  })

  it('sends an explicit null for an inherited alignment field', async () => {
    getCustomization.mockResolvedValueOnce({ overrides: { cache_control_limit_mode: 'trim', request_id_mode: 'fill' } })
    const wrapper = mountModal()
    await flushPromises()
    const select = wrapper.get('[data-field="cache_control_limit_mode"]').getComponent(Select)
    expect(select.props('modelValue')).toBe('trim')
    select.vm.$emit('update:modelValue', null)
    await saveName(wrapper)
    expect(updateCustomization).toHaveBeenCalledWith(1, { cache_control_limit_mode: null })
  })

  it.each(alignmentFields)('keeps an existing $key override when the schema fails and a legacy override is edited', async (field) => {
    getSchema.mockRejectedValueOnce(new Error('schema unavailable'))
    getCustomization.mockResolvedValueOnce({ overrides: { [field.key]: field.official_value } })
    const wrapper = mountModal()
    await flushPromises()
    expect(getSchema).toHaveBeenCalledOnce()
    expect(wrapper.findComponent({ name: 'OfficialAlignmentCard' }).exists()).toBe(false)
    await fallback(wrapper).setValue('strict')
    await saveName(wrapper)
    expect(updateCustomization).toHaveBeenCalledWith(1, { fallback_policy: 'strict' })
  })


  it.each(alignmentFields.filter(field => field.account_overridable))('preserves an existing $key override when another field changes', async (field) => {
    getCustomization.mockResolvedValueOnce({ overrides: { [field.key]: field.official_value } })
    const wrapper = mountModal()
    await flushPromises()
    await fallback(wrapper).setValue('strict')
    await saveName(wrapper)
    expect(updateCustomization).toHaveBeenCalledWith(1, { fallback_policy: 'strict' })
  })

  it('loads and edits an override introduced by a newer schema', async () => {
    getSchema.mockResolvedValueOnce([...schema, { ...alignmentFields[1], key: 'future_field' }])
    getCustomization.mockResolvedValueOnce({ overrides: { future_field: 'fill' } })
    const wrapper = mountModal()
    await flushPromises()
    const select = wrapper.get('[data-field="future_field"]').getComponent(Select)
    expect(select.props('modelValue')).toBe('fill')
    select.vm.$emit('update:modelValue', null)
    await saveName(wrapper)
    expect(updateCustomization).toHaveBeenCalledWith(1, { future_field: null })
  })

  it('drops a legacy models_catalog override when saving another override', async () => {
    getCustomization.mockResolvedValueOnce({ overrides: { models_catalog: 'official', request_id_mode: 'fill' } })
    const wrapper = mountModal()
    await flushPromises()
    await fallback(wrapper).setValue('strict')
    await saveName(wrapper)
    expect(updateCustomization).toHaveBeenCalledWith(1, { fallback_policy: 'strict' })
  })

  it.each([
    { account_overridable: false, global_only: false },
    { account_overridable: true, global_only: true },
  ])('filters loaded schema restrictions %j beyond the alignment group', async (restriction) => {
    getSchema.mockResolvedValueOnce(schema.map(field => field.key === 'fallback_policy' ? { ...field, ...restriction } : field))
    getCustomization.mockResolvedValueOnce({ overrides: { fallback_policy: 'strict' } })
    const wrapper = mountModal()
    await flushPromises()
    wrapper.get('[data-field="request_id_mode"]').getComponent(Select).vm.$emit('update:modelValue', 'fill')
    await saveName(wrapper)
    expect(updateCustomization).toHaveBeenCalledWith(1, { request_id_mode: 'fill' })
  })

  it('requests the schema only once per open dialog and resets account-specific card data', async () => {
    const old = deferred<{ overrides: Record<string, string>, global: Record<string, string> }>()
    getCustomization.mockReturnValueOnce(old.promise).mockResolvedValueOnce({
      overrides: { request_id_mode: 'fill' }, global: { ...alignmentDefaults, models_catalog: 'official' }, hits: null,
    })
    const wrapper = mountModal()
    await wrapper.setProps({ account: account(2), accountEtag: 'etag-2' })
    await flushPromises()
    old.resolve({ overrides: { cache_control_limit_mode: 'reject' }, global: alignmentDefaults })
    await flushPromises()
    const card = wrapper.getComponent({ name: 'OfficialAlignmentCard' })
    expect(card.props('globalValues')).toMatchObject({ models_catalog: 'official' })
    expect(card.props('modelValue')).toMatchObject({ request_id_mode: 'fill', cache_control_limit_mode: null })
    expect(card.props('hits')).toBeNull()
    expect(getSchema).toHaveBeenCalledTimes(1)
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(getSchema).toHaveBeenCalledTimes(2)
  })

  it('filters an API-key-only edit when saving an OAuth account', async () => {
    const key = 'context_management_missing_beta'
    getSchema.mockResolvedValueOnce(schema.map(field => field.key === key ? { ...field, account_types: ['apikey'] } : field))
    const wrapper = mountModal(1, 'anthropic', 'oauth')
    await flushPromises()
    const select = wrapper.get('[data-field="' + key + '"]').getComponent(Select)
    expect(select.props('disabled')).toBe(true)
    // Exercise submit validation independently of the disabled UI control.
    select.vm.$emit('update:modelValue', 'reject')
    await fallback(wrapper).setValue('strict')
    await saveName(wrapper)
    expect(updateCustomization).toHaveBeenCalledWith(1, { fallback_policy: 'strict' })
  })

  it('removes an ignored override from an OAuth account', async () => {
    const key = 'context_management_missing_beta'
    getSchema.mockResolvedValueOnce(schema.map(field => field.key === key ? { ...field, account_types: ['apikey'] } : field))
    getCustomization.mockResolvedValueOnce({ overrides: { [key]: 'reject' }, ignored_overrides: [key] })
    const wrapper = mountModal(1, 'anthropic', 'oauth')
    await flushPromises()
    await wrapper.get('[data-restore-inheritance]').trigger('click')
    await saveName(wrapper)
    expect(updateCustomization).toHaveBeenCalledWith(1, { [key]: null })
  })

  it('skips the card and schema request for non-Anthropic accounts', async () => {
    const wrapper = mountModal(1, 'openai')
    await flushPromises()
    expect(wrapper.findComponent({ name: 'OfficialAlignmentCard' }).exists()).toBe(false)
    expect(getSchema).not.toHaveBeenCalled()
  })
})
