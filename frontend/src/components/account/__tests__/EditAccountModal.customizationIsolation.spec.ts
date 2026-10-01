import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, shallowMount, type VueWrapper } from '@vue/test-utils'
import type { Account } from '@/types'

const { getCustomization, updateCustomization, updateAccount } = vi.hoisted(() => ({
  getCustomization: vi.fn(), updateCustomization: vi.fn(), updateAccount: vi.fn()
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn() }) }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ isOwner: true, isVendor: false, isSimpleMode: true }) }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: { getClaudeCustomization: getCustomization, updateClaudeCustomization: updateCustomization, update: updateAccount, checkMixedChannelRisk: vi.fn().mockResolvedValue({ has_risk: false }) },
    settings: { getWebSearchEmulationConfig: vi.fn().mockResolvedValue({ enabled: false, providers: [] }), getSettings: vi.fn().mockResolvedValue({}) },
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
function mountModal(id = 1) {
  const wrapper = shallowMount(EditAccountModal, {
    props: { show: true, account: account(id), proxies: [], groups: [] },
    global: { stubs: { BaseDialog: defineComponent({ template: '<div><slot /><slot name="footer" /></div>' }) } }
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
    getCustomization.mockReset().mockResolvedValue({ overrides: {} })
    updateCustomization.mockResolvedValue({ overrides: {} })
    updateAccount.mockImplementation(async (id: number) => account(id))
  })
  afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()) })

  it('does not write the previous account policy when the next response omits overrides', async () => {
    getCustomization.mockResolvedValueOnce({ overrides: { fallback_policy: 'strict' } }).mockResolvedValueOnce({})
    const wrapper = mountModal()
    await flushPromises()
    expect((fallback(wrapper).element as HTMLSelectElement).value).toBe('strict')
    await wrapper.setProps({ account: account(2) })
    await flushPromises()
    expect((fallback(wrapper).element as HTMLSelectElement).value).not.toBe('strict')
    await saveName(wrapper)
    expect(updateAccount).toHaveBeenCalledWith(2, expect.objectContaining({ name: 'Renamed' }))
    expect(updateCustomization).not.toHaveBeenCalled()
  })

  it('ignores late policy responses from another account', async () => {
    const first = deferred<{ overrides: Record<string, unknown> }>()
    getCustomization.mockReturnValueOnce(first.promise).mockResolvedValueOnce({ overrides: { fallback_policy: 'native_passthrough' } })
    const wrapper = mountModal()
    await wrapper.setProps({ account: account(2) })
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
    expect(updateAccount).toHaveBeenCalledTimes(1)
    expect(updateCustomization).not.toHaveBeenCalled()
    expect(wrapper.emitted('updated')).toHaveLength(1)
  })

  it('does not clear server policies when saving before policy loading completes', async () => {
    const pending = deferred<{ overrides: Record<string, unknown> }>()
    getCustomization.mockReturnValueOnce(pending.promise)
    const wrapper = mountModal()
    await saveName(wrapper)
    expect(updateAccount).toHaveBeenCalledTimes(1)
    expect(updateCustomization).not.toHaveBeenCalled()
    pending.resolve({ overrides: { fallback_policy: 'strict' } })
    await flushPromises()
  })

  it('persists an explicit edit and freezes it before the account save awaits', async () => {
    getCustomization.mockResolvedValueOnce({ overrides: { fallback_policy: 'strict' } }).mockResolvedValueOnce({ overrides: { fallback_policy: 'fable_native_passthrough' } })
    const pendingSave = deferred<Account>()
    updateAccount.mockReturnValueOnce(pendingSave.promise)
    const wrapper = mountModal()
    await flushPromises()
    await fallback(wrapper).setValue('native_passthrough')
    await wrapper.get('#edit-account-form').trigger('submit')
    await wrapper.setProps({ account: account(2) })
    await flushPromises()
    pendingSave.resolve(account(1))
    await flushPromises()
    expect(updateCustomization).toHaveBeenCalledWith(1, { fallback_policy: 'native_passthrough' })
  })

  it('restores inheritance by removing the override instead of sending an invalid null enum', async () => {
    getCustomization.mockResolvedValueOnce({ overrides: { fallback_policy: 'strict' } })
    const wrapper = mountModal()
    await flushPromises()
    await fallback(wrapper).setValue('')
    await saveName(wrapper)
    expect(updateCustomization).toHaveBeenCalledWith(1, {})
  })
})
