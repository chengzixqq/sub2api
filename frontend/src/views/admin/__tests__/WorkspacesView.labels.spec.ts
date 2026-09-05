import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import enWorkspaceMessages from '@/i18n/locales/en/admin/workspaces'
import zhWorkspaceMessages from '@/i18n/locales/zh/admin/workspaces'
import WorkspacesView from '../WorkspacesView.vue'

const { listWorkspaces, translatedLabels } = vi.hoisted(() => ({
  listWorkspaces: vi.fn(),
  translatedLabels: {
    'admin.workspaces.statusActive': 'localized-status-active',
    'admin.workspaces.statusDisabled': 'localized-status-disabled',
    'admin.workspaces.noPermissions': 'localized-no-permissions',
    'admin.workspaces.perms.accountManage': 'localized-account-manage',
    'admin.workspaces.perms.groupOps': 'localized-group-ops',
    'admin.workspaces.perms.groupBilling': 'localized-group-billing',
    'admin.workspaces.perms.proxyManage': 'localized-proxy-manage',
    'admin.workspaces.perms.monitorView': 'localized-monitor-view'
  } as Record<string, string>
}))

vi.mock('@/api/admin/workspaces', async () => {
  const actual = await vi.importActual<typeof import('@/api/admin/workspaces')>(
    '@/api/admin/workspaces'
  )
  return {
    ...actual,
    adminWorkspacesAPI: {
      ...actual.adminWorkspacesAPI,
      list: listWorkspaces
    }
  }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn()
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => translatedLabels[key] ?? key
    })
  }
})

const DataTableStub = {
  props: ['data'],
  template: `
    <div>
      <div v-for="row in data" :key="row.id">
        <slot name="cell-status" :row="row" />
        <slot name="cell-permissions" :row="row" />
      </div>
    </div>
  `
}

describe('WorkspacesView localized labels', () => {
  beforeEach(() => {
    listWorkspaces.mockReset()
    listWorkspaces.mockResolvedValue([
      {
        id: 1,
        name: 'Owner workspace',
        description: '',
        status: 'active',
        permissions: {
          account_manage: true,
          group_ops: true,
          group_billing: true,
          proxy_manage: true,
          monitor_view: true
        },
        settlement_rate_min: null,
        settlement_rate_max: null,
        created_at: 0,
        updated_at: 0
      },
      {
        id: 2,
        name: 'Disabled workspace',
        description: '',
        status: 'disabled',
        permissions: {
          account_manage: false,
          group_ops: false,
          group_billing: false,
          proxy_manage: false,
          monitor_view: false
        },
        settlement_rate_min: null,
        settlement_rate_max: null,
        created_at: 0,
        updated_at: 0
      }
    ])
  })

  it('maps API status and permission keys to existing locale entries', async () => {
    for (const messages of [zhWorkspaceMessages.workspaces, enWorkspaceMessages.workspaces]) {
      expect(messages.statusActive).toBeTruthy()
      expect(messages.statusDisabled).toBeTruthy()
      expect(messages.noPermissions).toBeTruthy()
      expect(messages.perms.accountManage).toBeTruthy()
      expect(messages.perms.groupOps).toBeTruthy()
      expect(messages.perms.groupBilling).toBeTruthy()
      expect(messages.perms.proxyManage).toBeTruthy()
      expect(messages.perms.monitorView).toBeTruthy()
    }

    const wrapper = mount(WorkspacesView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: {
            template: '<div><slot name="filters" /><slot name="table" /></div>'
          },
          DataTable: DataTableStub,
          ConfirmDialog: true,
          WorkspaceFormDialog: true,
          WorkspaceMembersDialog: true,
          WorkspaceGrantsDialog: true
        }
      }
    })

    await flushPromises()

    const text = wrapper.text()
    for (const label of Object.values(translatedLabels)) {
      expect(text).toContain(label)
    }
    expect(text).not.toContain('admin.workspaces.status.')
    expect(text).not.toContain('admin.workspaces.perms.')
  })
})
