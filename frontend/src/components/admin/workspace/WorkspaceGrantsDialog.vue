<template>
  <BaseDialog
    :show="true"
    :title="t('admin.workspaces.grantsTitle', { name: workspace.name })"
    width="wide"
    @close="emit('close')"
  >
    <div class="space-y-5">
      <!-- 授权表单：只定「能用哪个分组、以什么基准优先级」。
           结算倍率不在这里 —— 倍率挂在账号上，可调区间挂在工作区上
           （见工作区编辑表单的 settlement_rate_min/max）。一个账号可同时
           属于多个分组，若区间按分组设，同一账号会摊上多个上限，无从校验。 -->
      <div class="grid gap-3 rounded-lg border border-gray-200 p-3 sm:grid-cols-2 dark:border-dark-600">
        <div>
          <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">
            {{ t('admin.workspaces.grant.group') }}
          </label>
          <Select v-model="form.group_id" :options="groupOptions" value-key="value" label-key="label" />
        </div>
        <div>
          <label class="mb-1 block text-xs font-medium text-gray-600 dark:text-gray-400">
            {{ t('admin.workspaces.grant.basePriority') }}
          </label>
          <input v-model.number="form.base_priority" type="number" class="input" />
        </div>
        <label class="flex items-center gap-2 text-sm sm:col-span-2">
          <input v-model="form.enabled" type="checkbox" class="h-4 w-4 rounded border-gray-300 text-primary-600" />
          <span class="text-gray-700 dark:text-gray-300">{{ t('admin.workspaces.grant.enabled') }}</span>
        </label>
        <div class="sm:col-span-2">
          <button class="btn btn-primary" :disabled="saving || !form.group_id" @click="submit">
            {{ t('admin.workspaces.grant.save') }}
          </button>
        </div>
      </div>

      <div v-if="loading" class="py-6 text-center text-sm text-gray-500">{{ t('common.loading') }}</div>
      <div v-else-if="!grants.length" class="py-6 text-center text-sm text-gray-500">
        {{ t('admin.workspaces.grant.none') }}
      </div>
      <table v-else class="w-full text-sm">
        <thead class="text-left text-xs text-gray-500 dark:text-gray-400">
          <tr>
            <th class="py-2">{{ t('admin.workspaces.grant.group') }}</th>
            <th class="py-2">{{ t('admin.workspaces.grant.basePriority') }}</th>
            <th class="py-2">{{ t('admin.workspaces.columns.status') }}</th>
            <th class="py-2"></th>
          </tr>
        </thead>
        <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
          <tr v-for="g in grants" :key="g.id">
            <td class="py-2">{{ groupName(g.group_id) }}</td>
            <td class="py-2">{{ g.base_priority }}</td>
            <td class="py-2">
              <span :class="g.enabled ? 'text-green-600' : 'text-gray-400'">
                {{ g.enabled ? t('admin.workspaces.statusActive') : t('admin.workspaces.statusDisabled') }}
              </span>
            </td>
            <td class="py-2 text-right">
              <button class="mr-3 text-primary-600 hover:underline" @click="edit(g)">
                {{ t('common.edit') }}
              </button>
              <button class="text-red-600 hover:underline" @click="remove(g.group_id)">
                {{ t('common.delete') }}
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <template #footer>
      <button class="btn btn-secondary" @click="emit('close')">{{ t('common.close') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import {
  adminWorkspacesAPI,
  type Workspace,
  type WorkspaceGrant
} from '@/api/admin/workspaces'
import groupsAPI from '@/api/admin/groups'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import { useAppStore } from '@/stores/app'

const props = defineProps<{ workspace: Workspace }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const { t } = useI18n()
const appStore = useAppStore()

const grants = ref<WorkspaceGrant[]>([])
const groups = ref<Array<{ id: number; name: string }>>([])
const loading = ref(false)
const saving = ref(false)

const form = reactive({
  group_id: null as number | null,
  base_priority: 50,
  enabled: true
})

const groupOptions = computed(() =>
  groups.value.map((g) => ({ value: g.id, label: g.name }))
)

function groupName(id: number) {
  return groups.value.find((g) => g.id === id)?.name ?? `#${id}`
}

async function reload() {
  loading.value = true
  try {
    grants.value = await adminWorkspacesAPI.listGrants(props.workspace.id)
  } catch (err: any) {
    appStore.showError(err?.message || t('admin.workspaces.loadFailed'))
  } finally {
    loading.value = false
  }
}

async function loadGroups() {
  try {
    // 含停用分组：站长可能先建授权再启用分组。
    const list = await groupsAPI.getAllIncludingInactive()
    groups.value = list.map((g) => ({ id: g.id, name: g.name }))
  } catch {
    // 分组名拉取失败不阻断授权列表展示，回退为 #id 显示。
  }
}

function edit(g: WorkspaceGrant) {
  form.group_id = g.group_id
  form.base_priority = g.base_priority
  form.enabled = g.enabled
}

async function submit() {
  if (!form.group_id) return
  saving.value = true
  try {
    await adminWorkspacesAPI.upsertGrant(props.workspace.id, {
      group_id: form.group_id,
      base_priority: form.base_priority,
      enabled: form.enabled
    })
    appStore.showSuccess(t('common.saveSuccess'))
    reload()
  } catch (err: any) {
    appStore.showError(err?.message || t('common.saveFailed'))
  } finally {
    saving.value = false
  }
}

async function remove(groupID: number) {
  try {
    await adminWorkspacesAPI.removeGrant(props.workspace.id, groupID)
    appStore.showSuccess(t('common.deleteSuccess'))
    reload()
  } catch (err: any) {
    appStore.showError(err?.message || t('common.deleteFailed'))
  }
}

onMounted(() => {
  reload()
  loadGroups()
})
</script>
