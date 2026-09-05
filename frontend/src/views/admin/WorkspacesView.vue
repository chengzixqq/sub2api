<template>
  <AppLayout>
    <TablePageLayout>
      <template #filters>
        <!-- 三步流程写在页面上：光看列表看不出「建完之后供应商怎么进来」，
             而这三步跨了两个页面（授权在此、角色在用户管理），最容易卡住。 -->
        <div class="mb-3 rounded-lg bg-blue-50 px-3 py-2.5 text-xs leading-relaxed text-blue-800 dark:bg-blue-900/20 dark:text-blue-300">
          {{ t('admin.workspaces.usageHint') }}
        </div>
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">
              {{ t('admin.workspaces.title') }}
            </h2>
            <p class="mt-0.5 text-sm text-gray-500 dark:text-gray-400">
              {{ t('admin.workspaces.description') }}
            </p>
          </div>
          <div class="flex items-center gap-2">
            <button type="button" class="btn btn-secondary" :disabled="loading" @click="reload">
              {{ t('common.refresh') }}
            </button>
            <button type="button" class="btn btn-primary" @click="openCreate">
              {{ t('admin.workspaces.create') }}
            </button>
          </div>
        </div>
      </template>

      <template #table>
        <DataTable :columns="columns" :data="workspaces" :loading="loading" row-key="id">
          <template #cell-name="{ row }">
            <div class="min-w-0">
              <div class="truncate font-medium text-gray-900 dark:text-white">{{ row.name }}</div>
              <div v-if="row.description" class="truncate text-xs text-gray-500 dark:text-gray-400">
                {{ row.description }}
              </div>
            </div>
          </template>

          <template #cell-status="{ row }">
            <span
              class="inline-flex items-center rounded-md px-2 py-0.5 text-xs font-medium"
              :class="
                row.status === 'active'
                  ? 'bg-green-50 text-green-700 dark:bg-green-900/30 dark:text-green-300'
                  : 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
              "
            >
              {{ workspaceStatusLabel(row.status) }}
            </span>
          </template>

          <template #cell-permissions="{ row }">
            <div class="flex flex-wrap gap-1">
              <span
                v-for="perm in enabledPerms(row.permissions)"
                :key="perm"
                class="inline-flex items-center rounded bg-primary-50 px-1.5 py-0.5 text-xs text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
              >
                {{ workspacePermissionLabel(perm) }}
              </span>
              <span v-if="enabledPerms(row.permissions).length === 0" class="text-xs text-gray-400">
                {{ t('admin.workspaces.noPermissions') }}
              </span>
            </div>
          </template>

          <template #cell-actions="{ row }">
            <div class="flex items-center gap-1">
              <button type="button" class="btn btn-ghost btn-sm" @click="openEdit(row)">
                {{ t('common.edit') }}
              </button>
              <button type="button" class="btn btn-ghost btn-sm" @click="openMembers(row)">
                {{ t('admin.workspaces.members') }}
              </button>
              <button type="button" class="btn btn-ghost btn-sm" @click="openGrants(row)">
                {{ t('admin.workspaces.grants') }}
              </button>
              <button type="button" class="btn btn-ghost btn-sm text-red-600" @click="confirmRemove(row)">
                {{ t('common.delete') }}
              </button>
            </div>
          </template>
        </DataTable>
      </template>
    </TablePageLayout>

    <WorkspaceFormDialog
      v-if="formOpen"
      :workspace="editing"
      @close="formOpen = false"
      @saved="handleSaved"
    />
    <WorkspaceMembersDialog
      v-if="membersFor"
      :workspace="membersFor"
      @close="membersFor = null"
    />
    <WorkspaceGrantsDialog
      v-if="grantsFor"
      :workspace="grantsFor"
      @close="grantsFor = null"
    />

    <ConfirmDialog
      :show="!!pendingDelete"
      danger
      :title="t('admin.workspaces.confirmDeleteTitle')"
      :message="t('admin.workspaces.confirmDelete', { name: pendingDelete?.name ?? '' })"
      @confirm="performDelete"
      @cancel="pendingDelete = null"
    />
  </AppLayout>
</template>
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import {
  adminWorkspacesAPI,
  type Workspace,
  type WorkspacePermissions
} from '@/api/admin/workspaces'
import type { Column } from '@/components/common/types'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import WorkspaceFormDialog from '@/components/admin/workspace/WorkspaceFormDialog.vue'
import WorkspaceMembersDialog from '@/components/admin/workspace/WorkspaceMembersDialog.vue'
import WorkspaceGrantsDialog from '@/components/admin/workspace/WorkspaceGrantsDialog.vue'
import { useAppStore } from '@/stores/app'

const { t } = useI18n()
const appStore = useAppStore()

const workspaces = ref<Workspace[]>([])
const loading = ref(false)

const formOpen = ref(false)
const editing = ref<Workspace | null>(null)
const membersFor = ref<Workspace | null>(null)
const grantsFor = ref<Workspace | null>(null)

const columns = computed<Column[]>(() => [
  { key: 'name', label: t('admin.workspaces.columns.name'), sortable: true },
  { key: 'status', label: t('admin.workspaces.columns.status') },
  { key: 'permissions', label: t('admin.workspaces.columns.permissions') },
  { key: 'actions', label: t('common.actions') }
])

const workspaceStatusLocaleKeys: Record<Workspace['status'], string> = {
  active: 'admin.workspaces.statusActive',
  disabled: 'admin.workspaces.statusDisabled'
}

const workspacePermissionLocaleKeys: Record<keyof WorkspacePermissions, string> = {
  account_manage: 'admin.workspaces.perms.accountManage',
  group_ops: 'admin.workspaces.perms.groupOps',
  group_billing: 'admin.workspaces.perms.groupBilling',
  proxy_manage: 'admin.workspaces.perms.proxyManage',
  monitor_view: 'admin.workspaces.perms.monitorView'
}

function workspaceStatusLabel(status: Workspace['status']): string {
  return t(workspaceStatusLocaleKeys[status])
}

function workspacePermissionLabel(permission: keyof WorkspacePermissions): string {
  return t(workspacePermissionLocaleKeys[permission])
}

/** 只展示已开启的权限档，关闭项不占视觉空间。 */
function enabledPerms(perms: WorkspacePermissions): Array<keyof WorkspacePermissions> {
  return (Object.keys(perms) as Array<keyof WorkspacePermissions>).filter((k) => perms[k])
}

async function reload() {
  loading.value = true
  try {
    workspaces.value = await adminWorkspacesAPI.list()
  } catch (err: any) {
    appStore.showError(err?.message || t('admin.workspaces.loadFailed'))
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editing.value = null
  formOpen.value = true
}

function openEdit(row: Workspace) {
  editing.value = row
  formOpen.value = true
}

function openMembers(row: Workspace) {
  membersFor.value = row
}

function openGrants(row: Workspace) {
  grantsFor.value = row
}

function handleSaved() {
  formOpen.value = false
  reload()
}

// 删除确认：工作区被删后其成员立即失去后台访问，
// 影响面较大，走与全站一致的确认弹窗而非静默执行。
const pendingDelete = ref<Workspace | null>(null)
const deleting = ref(false)

function confirmRemove(row: Workspace) {
  pendingDelete.value = row
}

async function performDelete() {
  const target = pendingDelete.value
  if (!target) return
  deleting.value = true
  try {
    await adminWorkspacesAPI.remove(target.id)
    appStore.showSuccess(t('admin.workspaces.deleted'))
    pendingDelete.value = null
    reload()
  } catch (err: any) {
    appStore.showError(err?.message || t('admin.workspaces.deleteFailed'))
  } finally {
    deleting.value = false
  }
}

onMounted(reload)
</script>
