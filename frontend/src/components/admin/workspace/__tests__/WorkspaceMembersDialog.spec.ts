import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import WorkspaceMembersDialog from '../WorkspaceMembersDialog.vue'

const { listMembers, addMember, removeMember, listUsers, showSuccess, showError } = vi.hoisted(
  () => ({
    listMembers: vi.fn(),
    addMember: vi.fn(),
    removeMember: vi.fn(),
    listUsers: vi.fn(),
    showSuccess: vi.fn(),
    showError: vi.fn()
  })
)

vi.mock('@/api/admin/workspaces', () => ({
  adminWorkspacesAPI: { listMembers, addMember, removeMember }
}))

vi.mock('@/api/admin/users', () => ({
  default: { list: listUsers },
  usersAPI: { list: listUsers }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess, showError })
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    // 回显键名与插值，断言才能落在具体文案键上而非渲染结果。
    t: (key: string, params?: Record<string, unknown>) =>
      params ? `${key}:${JSON.stringify(params)}` : key
  })
}))

const BaseDialogStub = defineComponent({
  template: '<div><slot /><slot name="footer" /></div>'
})

function mountDialog() {
  return mount(WorkspaceMembersDialog, {
    props: { workspace: { id: 7, name: 'A 家' } as never },
    global: { stubs: { BaseDialog: BaseDialogStub } }
  })
}

/** 驱动 250ms 防抖 + 随后的异步搜索。 */
async function typeAndSettle(wrapper: ReturnType<typeof mountDialog>, keyword: string) {
  await wrapper.get('input[type="text"]').setValue(keyword)
  await vi.advanceTimersByTimeAsync(250)
  await flushPromises()
}

describe('WorkspaceMembersDialog', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    for (const fn of [listMembers, addMember, removeMember, listUsers, showSuccess, showError]) {
      fn.mockReset()
    }
    listMembers.mockResolvedValue([
      { id: 1, user_id: 11, email: 'bound@x.com', username: 'bound', role: 'vendor' }
    ])
    listUsers.mockResolvedValue({ items: [], total: 0 })
  })

  // 全局角色被顺带改动必须显式回报。静默提升会让站长在
  // 用户管理页看到角色变了却找不到原因。
  it('提升普通用户为供应商时给出带用户名的专门提示', async () => {
    listUsers.mockResolvedValue({
      items: [{ id: 22, email: 'plain@x.com', username: '小王', role: 'user' }],
      total: 1
    })
    addMember.mockResolvedValue({ id: 2, user_id: 22, role_promoted: true })

    const wrapper = mountDialog()
    await flushPromises()
    await typeAndSettle(wrapper, '小王')

    await wrapper.get('[data-testid="member-candidate-22"]').trigger('click')
    await flushPromises()

    expect(addMember).toHaveBeenCalledWith(7, 22)
    expect(showSuccess).toHaveBeenCalledWith(
      'admin.workspaces.memberAddPromoted:{"name":"小王"}'
    )
    wrapper.unmount()
  })

  // 已是 vendor 的用户不发生角色变更，走普通成功提示。
  it('绑定既有供应商时不提示角色变更', async () => {
    listUsers.mockResolvedValue({
      items: [{ id: 33, email: 'v@x.com', username: 'v', role: 'vendor' }],
      total: 1
    })
    addMember.mockResolvedValue({ id: 3, user_id: 33 })

    const wrapper = mountDialog()
    await flushPromises()
    await typeAndSettle(wrapper, 'v')

    await wrapper.get('[data-testid="member-candidate-33"]').trigger('click')
    await flushPromises()

    expect(showSuccess).toHaveBeenCalledWith('admin.workspaces.memberAdded')
    wrapper.unmount()
  })

  // 降级站长不可逆，且可能降掉最后一个 admin 而锁死后台。
  // 后端会拒，前端不该让人白点一次。
  it('候选列表剔除站长与已绑定成员', async () => {
    listUsers.mockResolvedValue({
      items: [
        { id: 99, email: 'owner@x.com', username: 'owner', role: 'admin' },
        { id: 11, email: 'bound@x.com', username: 'bound', role: 'vendor' },
        { id: 44, email: 'ok@x.com', username: 'ok', role: 'user' }
      ],
      total: 3
    })

    const wrapper = mountDialog()
    await flushPromises()
    await typeAndSettle(wrapper, 'o')

    expect(wrapper.find('[data-testid="member-candidate-99"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="member-candidate-11"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="member-candidate-44"]').exists()).toBe(true)
    wrapper.unmount()
  })

  // 勾选状态变了而候选还是旧的，看起来像开关没生效。
  it('切换「只看供应商」立即用当前关键词重搜', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    await typeAndSettle(wrapper, 'abc')
    expect(listUsers).toHaveBeenCalledTimes(1)
    expect(listUsers.mock.calls[0][2]).toMatchObject({ role: 'vendor' })

    await wrapper.get('input[type="checkbox"]').setValue(false)
    await flushPromises()

    expect(listUsers).toHaveBeenCalledTimes(2)
    // 关闭后不得再传 role，否则普通用户永远搜不出来。
    expect(listUsers.mock.calls[1][2]).not.toHaveProperty('role')
    wrapper.unmount()
  })
})
