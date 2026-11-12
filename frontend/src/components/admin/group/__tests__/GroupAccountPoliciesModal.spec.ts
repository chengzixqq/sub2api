import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import type { AdminGroup } from '@/types'
import GroupAccountPoliciesModal from '../GroupAccountPoliciesModal.vue'

const mocks = vi.hoisted(() => ({
  getGroupAccountPolicies: vi.fn(),
  putGroupAccountPolicy: vi.fn(),
  deleteGroupAccountPolicy: vi.fn(),
  getAllIncludingInactive: vi.fn(),
  listAccounts: vi.fn(),
  listUsers: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: {
      getGroupAccountPolicies: mocks.getGroupAccountPolicies,
      putGroupAccountPolicy: mocks.putGroupAccountPolicy,
      deleteGroupAccountPolicy: mocks.deleteGroupAccountPolicy,
      getAllIncludingInactive: mocks.getAllIncludingInactive
    },
    accounts: { list: mocks.listAccounts },
    users: { list: mocks.listUsers }
  }
}))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: mocks.showError, showSuccess: mocks.showSuccess }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

enableAutoUnmount(afterEach)
beforeEach(() => {
  vi.resetAllMocks()
  mocks.getAllIncludingInactive.mockResolvedValue([group(1), group(2)])
})
afterEach(() => vi.restoreAllMocks())

const group = (id: number) => ({ id, name: `Group ${id}`, platform: 'openai' } as AdminGroup)
const policy = (userID: number, mode: 'append' | 'replace', accounts: Array<{ account_id: number; origin: 'member' | 'extra'; account_name?: string }> = []) => ({
  user_id: userID,
  group_id: 1,
  username: `user${userID}`,
  email: `user${userID}@example.com`,
  version: 3,
  mode,
  accounts
})

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(res => { resolve = res })
  return { promise, resolve }
}

async function openModal(entries = [policy(1, 'append')]) {
  mocks.getGroupAccountPolicies.mockResolvedValue(entries)
  mocks.listAccounts.mockResolvedValue({ items: [], pages: 1 })
  const wrapper = mount(GroupAccountPoliciesModal, {
    props: { show: false, group: group(1) },
    global: { stubs: {
      BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /></div>' },
      Icon: true,
      PlatformIcon: true
    } }
  })
  await wrapper.setProps({ show: true })
  await flushPromises()
  return wrapper
}

describe('group account policies', () => {
  it('preserves an empty replace policy and confirms blocking before saving', async () => {
    const existing = policy(1, 'replace')
    mocks.putGroupAccountPolicy.mockResolvedValue({ ...existing, version: 4 })
    const wrapper = await openModal([existing])
    await wrapper.findAll('button').find(button => button.text().includes('user1'))!.trigger('click')
    const save = wrapper.findAll('button').find(button => button.text() === 'common.save')!
    await save.trigger('click')
    expect(mocks.putGroupAccountPolicy).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('admin.groups.accountPolicyEmptyWarning')
    await wrapper.findAll('button').find(button => button.text() === 'admin.groups.accountPolicyConfirmEmpty')!.trigger('click')
    expect(mocks.putGroupAccountPolicy).toHaveBeenCalledWith(1, 1, 3, { mode: 'replace', accounts: [] })
  })

  it('uses DELETE with the current version to restore defaults', async () => {
    mocks.deleteGroupAccountPolicy.mockResolvedValue(undefined)
    const wrapper = await openModal([policy(1, 'replace')])
    await wrapper.findAll('button').find(button => button.text().includes('user1'))!.trigger('click')
    await wrapper.findAll('button').find(button => button.text() === 'admin.groups.accountPolicyRestore')!.trigger('click')
    expect(mocks.deleteGroupAccountPolicy).toHaveBeenCalledWith(1, 1, 3)
    expect(mocks.putGroupAccountPolicy).not.toHaveBeenCalled()
  })

  it('retains selected accounts across pages and marks outside accounts as extra', async () => {
    const wrapper = await openModal([policy(1, 'replace')])
    mocks.listAccounts.mockImplementation((page: number) => Promise.resolve(page === 1
      ? { items: [{ id: 10, name: 'member', group_ids: [1], dispatch_scope: 'shared' }], pages: 2 }
      : { items: [{ id: 20, name: 'test', group_ids: [2], dispatch_scope: 'authorized_only' }], pages: 2 }))
    mocks.putGroupAccountPolicy.mockResolvedValue(policy(1, 'replace', [
      { account_id: 10, origin: 'member' }, { account_id: 20, origin: 'extra' }
    ]))
    await wrapper.findAll('button').find(button => button.text().includes('user1'))!.trigger('click')
    await new Promise(resolve => setTimeout(resolve, 250))
    await flushPromises()
    expect(wrapper.text()).toContain('admin.groups.accountPolicyGroups: Group 1')
    await wrapper.get('label input[type="checkbox"]').setValue(true)
    await wrapper.get('button[title="common.next"]').trigger('click')
    await new Promise(resolve => setTimeout(resolve, 250))
    await flushPromises()
    expect(wrapper.text()).toContain('admin.groups.accountPolicyGroups: Group 2')
    expect(mocks.getAllIncludingInactive).toHaveBeenCalledTimes(1)
    await wrapper.get('label input[type="checkbox"]').setValue(true)
    await wrapper.findAll('button').find(button => button.text() === 'common.save')!.trigger('click')
    expect(mocks.putGroupAccountPolicy).toHaveBeenCalledWith(1, 1, 3, {
      mode: 'replace',
      accounts: [{ account_id: 10, origin: 'member' }, { account_id: 20, origin: 'extra' }]
    })
  })

  it('shows every source group name and explicit ungrouped text from one directory request', async () => {
    const wrapper = await openModal()
    mocks.listAccounts.mockResolvedValue({ items: [
      { id: 10, name: 'multi-group', group_ids: [1, 2], dispatch_scope: 'shared' },
      { id: 20, name: 'private-test', group_ids: [], dispatch_scope: 'authorized_only' }
    ], pages: 1 })
    await wrapper.findAll('button').find(button => button.text().includes('user1'))!.trigger('click')
    await new Promise(resolve => setTimeout(resolve, 250))
    await flushPromises()
    const rows = wrapper.findAll('label').filter(label => label.find('input[type="checkbox"]').exists())
    expect(rows[0]?.text()).toContain('admin.groups.accountPolicyGroups: Group 1, Group 2')
    expect(rows[1]?.text()).toContain('admin.groups.accountPolicyGroups: admin.groups.accountPolicyUngrouped')
    expect(mocks.getAllIncludingInactive).toHaveBeenCalledTimes(1)
  })

  it.each(['save', 'restore'] as const)('requires reselecting the latest policy after a %s version conflict', async action => {
    const stale = policy(1, 'append', [{ account_id: 10, origin: 'member', account_name: 'old-account' }])
    const latest = { ...policy(1, 'replace', [{ account_id: 20, origin: 'extra', account_name: 'latest-account' }]), version: 4 }
    const wrapper = await openModal([stale])
    await wrapper.findAll('button').find(button => button.text().includes('user1'))!.trigger('click')
    mocks.getGroupAccountPolicies.mockResolvedValue([latest])
    if (action === 'save') mocks.putGroupAccountPolicy.mockRejectedValueOnce({ status: 412 })
    else mocks.deleteGroupAccountPolicy.mockRejectedValueOnce({ status: 412 })
    const actionLabel = action === 'save' ? 'common.save' : 'admin.groups.accountPolicyRestore'
    await wrapper.findAll('button').find(button => button.text() === actionLabel)!.trigger('click')
    await flushPromises()
    expect(mocks.showError).toHaveBeenCalledWith('admin.groups.accountPolicyConflict')
    expect(wrapper.text()).toContain('admin.groups.accountPolicyChooseUser')
    expect(wrapper.findAll('button').some(button => button.text() === 'common.save')).toBe(false)
    expect(wrapper.text()).not.toContain('old-account')

    await wrapper.findAll('button').find(button => button.text().includes('user1'))!.trigger('click')
    expect(wrapper.text()).toContain('latest-account')
    expect(wrapper.text()).not.toContain('old-account')
    mocks.putGroupAccountPolicy.mockResolvedValue({ ...latest, version: 5 })
    await wrapper.findAll('button').find(button => button.text() === 'common.save')!.trigger('click')
    expect(mocks.putGroupAccountPolicy).toHaveBeenLastCalledWith(1, 1, 4, {
      mode: 'replace', accounts: [{ account_id: 20, origin: 'extra' }]
    })
  })

  it('does not advance an unchanged draft version after a failed write refreshes the list', async () => {
    const stale = policy(1, 'append', [{ account_id: 10, origin: 'member' }])
    const wrapper = await openModal([stale])
    await wrapper.findAll('button').find(button => button.text().includes('user1'))!.trigger('click')
    mocks.getGroupAccountPolicies.mockResolvedValue([{ ...policy(1, 'replace'), version: 4 }])
    mocks.putGroupAccountPolicy.mockRejectedValue({ status: 503 })
    await wrapper.findAll('button').find(button => button.text() === 'common.save')!.trigger('click')
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === 'common.save')!.trigger('click')
    expect(mocks.putGroupAccountPolicy).toHaveBeenLastCalledWith(1, 1, 3, {
      mode: 'append', accounts: [{ account_id: 10, origin: 'member' }]
    })
  })

  it('ignores a late response from the previous group', async () => {
    const old = deferred<ReturnType<typeof policy>[]>()
    mocks.getGroupAccountPolicies.mockReturnValueOnce(old.promise).mockResolvedValueOnce([policy(2, 'append')])
    const wrapper = mount(GroupAccountPoliciesModal, {
      props: { show: false, group: group(1) },
      global: { stubs: {
        BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /></div>' },
        Icon: true,
        PlatformIcon: true
      } }
    })
    await wrapper.setProps({ show: true })
    await wrapper.setProps({ group: group(2) })
    await flushPromises()
    old.resolve([policy(1, 'replace')])
    await flushPromises()
    expect(wrapper.text()).toContain('user2')
    expect(wrapper.text()).not.toContain('user1')
  })
})
