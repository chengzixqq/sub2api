import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import UserApiKeysModal from '../UserApiKeysModal.vue'
import type { AdminUser } from '@/types'

const { getKeys } = vi.hoisted(() => ({ getKeys: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: {
  users: { getUserApiKeys: getKeys }, groups: { getAll: vi.fn().mockResolvedValue([]) },
} }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() }) }))
vi.mock('@/utils/format', () => ({ formatDateTime: (value: string) => value }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
enableAutoUnmount(afterEach)
beforeEach(() => { getKeys.mockReset(); vi.spyOn(console, 'error').mockImplementation(() => {}) })
afterEach(() => vi.restoreAllMocks())
function deferred() {
  let resolve!: (value: unknown) => void
  let reject!: (error: Error) => void
  const promise = new Promise((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}
const user = (id: number) => ({ id, email: `user${id}@example.com`, username: `user${id}` }) as AdminUser
const keys = (id: number, name: string) => ({ items: [{ id, name, key: 'sk-example-key-value-for-tests', status: 'active', created_at: '2026-09-20', group_id: null }] })
async function open() {
  const wrapper = mount(UserApiKeysModal, {
    props: { show: false, user: user(1) },
    global: { stubs: {
      BaseDialog: { name: 'BaseDialog', props: ['show'], template: '<div v-if="show"><slot /></div>' },
      GroupBadge: true, GroupOptionItem: true,
    } },
  })
  await wrapper.setProps({ show: true })
  return wrapper
}
async function switchUser(wrapper: Awaited<ReturnType<typeof open>>) {
  await wrapper.setProps({ show: false })
  await wrapper.setProps({ show: true, user: user(2) })
}

describe('user API key loading', () => {
  it('cancels key requests when switching users, hiding the dialog, and unmounting', async () => {
    const first = deferred(); const second = deferred(); const third = deferred()
    getKeys.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise).mockReturnValueOnce(third.promise)
    const wrapper = await open()
    expect(getKeys).toHaveBeenLastCalledWith(1, { signal: expect.any(AbortSignal) })
    const firstSignal = getKeys.mock.calls[0][1].signal as AbortSignal
    expect(firstSignal.aborted).toBe(false)
    await wrapper.setProps({ user: user(2) })
    const secondSignal = getKeys.mock.calls[1][1].signal as AbortSignal
    expect(firstSignal.aborted).toBe(true)
    expect(secondSignal.aborted).toBe(false)
    await wrapper.setProps({ show: false })
    expect(secondSignal.aborted).toBe(true)
    await wrapper.setProps({ show: true, user: user(3) })
    const thirdSignal = getKeys.mock.calls[2][1].signal as AbortSignal
    expect(thirdSignal.aborted).toBe(false)
    wrapper.unmount()
    expect(thirdSignal.aborted).toBe(true)
    first.resolve(keys(1, 'old')); second.resolve(keys(2, 'old')); third.resolve(keys(3, 'current'))
    await flushPromises()
  })

  it('cancels immediately when the dialog emits close before its prop changes', async () => {
    const pending = deferred()
    getKeys.mockReturnValueOnce(pending.promise)
    const wrapper = await open()
    expect(getKeys).toHaveBeenLastCalledWith(1, { signal: expect.any(AbortSignal) })
    const signal = getKeys.mock.calls[0][1].signal as AbortSignal
    wrapper.findComponent({ name: 'BaseDialog' }).vm.$emit('close')
    await flushPromises()
    expect(signal.aborted).toBe(true)
    expect(wrapper.emitted('close')).toHaveLength(1)
    pending.resolve(keys(1, 'should-not-display'))
    await flushPromises()
    expect(wrapper.text()).not.toContain('should-not-display')
  })

  it('does not display the previous user keys when the next load fails', async () => {
    getKeys.mockResolvedValueOnce(keys(1, 'first-user-key')).mockRejectedValueOnce(new Error('unavailable'))
    const wrapper = await open(); await flushPromises()
    expect(wrapper.text()).toContain('first-user-key')
    await switchUser(wrapper); await flushPromises()
    expect(wrapper.text()).toContain('user2@example.com')
    expect(wrapper.text()).not.toContain('first-user-key')
  })

  it('does not replace current keys with a late previous response', async () => {
    const old = deferred()
    getKeys.mockReturnValueOnce(old.promise).mockResolvedValueOnce(keys(2, 'current-user-key'))
    const wrapper = await open()
    await switchUser(wrapper); await flushPromises()
    old.resolve(keys(1, 'old-user-key')); await flushPromises()
    expect(wrapper.text()).toContain('current-user-key')
    expect(wrapper.text()).not.toContain('old-user-key')
  })

  it('keeps the current request loading when an obsolete request fails', async () => {
    const old = deferred(); const current = deferred()
    getKeys.mockReturnValueOnce(old.promise).mockReturnValueOnce(current.promise)
    const wrapper = await open()
    await switchUser(wrapper)
    old.reject(new Error('obsolete')); await flushPromises()
    expect(wrapper.find('.animate-spin').exists()).toBe(true)
    current.resolve(keys(2, 'current-user-key')); await flushPromises()
    expect(wrapper.text()).toContain('current-user-key')
    expect(wrapper.find('.animate-spin').exists()).toBe(false)
  })

  it('loads keys when the selected user changes while the dialog is open', async () => {
    getKeys.mockResolvedValueOnce(keys(1, 'first-user-key')).mockResolvedValueOnce(keys(2, 'second-user-key'))
    const wrapper = await open(); await flushPromises()
    await wrapper.setProps({ user: user(2) }); await flushPromises()
    expect(getKeys).toHaveBeenLastCalledWith(2, { signal: expect.any(AbortSignal) })
    expect(wrapper.text()).toContain('second-user-key')
    expect(wrapper.text()).not.toContain('first-user-key')
  })
})
