<template>
  <BaseDialog
    :show="true"
    :title="isEdit ? t('admin.workspaces.editTitle') : t('admin.workspaces.createTitle')"
    width="normal"
    @close="emit('close')"
  >
    <div class="space-y-5">
      <div>
        <label class="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">
          {{ t('admin.workspaces.columns.name') }}
        </label>
        <input v-model.trim="form.name" type="text" class="input" maxlength="100" />
      </div>

      <div>
        <label class="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">
          {{ t('admin.workspaces.description') }}
        </label>
        <textarea v-model.trim="form.description" rows="2" class="input" />
      </div>

      <div v-if="isEdit">
        <label class="mb-1 block text-sm font-medium text-gray-700 dark:text-gray-300">
          {{ t('admin.workspaces.columns.status') }}
        </label>
        <Select
          v-model="form.status"
          :options="[
            { value: 'active', label: t('admin.workspaces.statusActive') },
            { value: 'disabled', label: t('admin.workspaces.statusDisabled') }
          ]"
        />
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
          {{ t('admin.workspaces.statusHint') }}
        </p>
      </div>

      <!-- 权限档：整档提交。未勾选即为关闭，不存在"保留原值" -->
      <div>
        <div class="mb-2 text-sm font-medium text-gray-700 dark:text-gray-300">
          {{ t('admin.workspaces.columns.permissions') }}
        </div>
        <div class="space-y-2 rounded-lg border border-gray-200 p-3 dark:border-dark-600">
          <label
            v-for="item in permissionItems"
            :key="item.key"
            class="flex cursor-pointer items-start gap-2.5"
          >
            <input
              v-model="form.permissions[item.key]"
              type="checkbox"
              class="mt-0.5 h-4 w-4 rounded border-gray-300 text-primary-600"
            />
            <span class="min-w-0">
              <span class="block text-sm text-gray-900 dark:text-gray-100">{{ item.label }}</span>
              <span class="block text-xs text-gray-500 dark:text-gray-400">{{ item.hint }}</span>
            </span>
          </label>
        </div>
      </div>

      <!--
        结算倍率可调区间。留空即该端不设限；两端都留空则对方完全不可自调。
        倍率本身挂在账号上，这里只定边界，对该工作区名下所有账号统一生效。
      -->
      <div>
        <div class="mb-2 text-sm font-medium text-gray-700 dark:text-gray-300">
          {{ t('admin.workspaces.settlementRange.label') }}
        </div>
        <div class="flex items-center gap-2">
          <input
            v-model="minInput"
            type="number"
            step="0.0001"
            min="0"
            class="input"
            :placeholder="t('admin.workspaces.settlementRange.minPlaceholder')"
          />
          <span class="text-gray-400">~</span>
          <input
            v-model="maxInput"
            type="number"
            step="0.0001"
            min="0"
            class="input"
            :placeholder="t('admin.workspaces.settlementRange.maxPlaceholder')"
          />
        </div>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
          {{ t('admin.workspaces.settlementRange.hint') }}
        </p>
      </div>
    </div>

    <template #footer>
      <button class="btn btn-secondary" @click="emit('close')">{{ t('common.cancel') }}</button>
      <button class="btn btn-primary" :disabled="saving || !form.name" @click="submit">
        {{ saving ? t('common.saving') : t('common.save') }}
      </button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'

import {
  adminWorkspacesAPI,
  type Workspace,
  type WorkspacePermissions
} from '@/api/admin/workspaces'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import { useAppStore } from '@/stores/app'

const props = defineProps<{ workspace: Workspace | null }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'saved'): void }>()

const { t } = useI18n()
const appStore = useAppStore()

const isEdit = computed(() => !!props.workspace)
const saving = ref(false)

function blankPermissions(): WorkspacePermissions {
  return {
    account_manage: false,
    group_ops: false,
    group_billing: false,
    proxy_manage: false,
    monitor_view: false
  }
}

const form = reactive({
  name: props.workspace?.name ?? '',
  description: props.workspace?.description ?? '',
  status: props.workspace?.status ?? 'active',
  permissions: { ...blankPermissions(), ...(props.workspace?.permissions ?? {}) }
})

// 区间边界用字符串暂存：空串代表"不设限"(null)，而 0 是合法边界，
// 不能用 falsy 判断混为一谈。类型含 number 是因为绑在
// <input type="number"> 上时 Vue 会把值转成 number，清空又回到空串。
const minInput = ref<string | number>(props.workspace?.settlement_rate_min ?? '')
const maxInput = ref<string | number>(props.workspace?.settlement_rate_max ?? '')

function parseBound(raw: unknown): number | null {
  if (raw === null || raw === undefined || raw === '') return null
  const parsed = typeof raw === 'number' ? raw : Number(String(raw).trim())
  return Number.isFinite(parsed) ? parsed : null
}

const permissionItems = computed<
  Array<{ key: keyof WorkspacePermissions; label: string; hint: string }>
>(() => [
  {
    key: 'account_manage',
    label: t('admin.workspaces.perms.accountManage'),
    hint: t('admin.workspaces.perms.accountManageHint')
  },
  {
    key: 'group_ops',
    label: t('admin.workspaces.perms.groupOps'),
    hint: t('admin.workspaces.perms.groupOpsHint')
  },
  {
    key: 'group_billing',
    label: t('admin.workspaces.perms.groupBilling'),
    hint: t('admin.workspaces.perms.groupBillingHint')
  },
  {
    key: 'proxy_manage',
    label: t('admin.workspaces.perms.proxyManage'),
    hint: t('admin.workspaces.perms.proxyManageHint')
  },
  {
    key: 'monitor_view',
    label: t('admin.workspaces.perms.monitorView'),
    hint: t('admin.workspaces.perms.monitorViewHint')
  }
])

async function submit() {
  if (!form.name) {
    appStore.showError(t('admin.workspaces.nameRequired'))
    return
  }
  const min = parseBound(minInput.value)
  const max = parseBound(maxInput.value)
  if ((min !== null && min < 0) || (max !== null && max < 0)) {
    appStore.showError(t('admin.workspaces.settlementRange.negative'))
    return
  }
  if (min !== null && max !== null && min > max) {
    appStore.showError(t('admin.workspaces.settlementRange.inverted'))
    return
  }
  saving.value = true
  try {
    if (props.workspace) {
      await adminWorkspacesAPI.update(props.workspace.id, {
        name: form.name,
        description: form.description,
        status: form.status,
        permissions: { ...form.permissions },
        // 清空输入要发 0 而非省略：后端 *float64 分不开 JSON null 与字段
        // 缺省，两者都解成 nil=不修改，那样边界就永远清不掉了。
        settlement_rate_min: min ?? 0,
        settlement_rate_max: max ?? 0
      })
    } else {
      await adminWorkspacesAPI.create({
        name: form.name,
        description: form.description,
        permissions: { ...form.permissions },
        settlement_rate_min: min,
        settlement_rate_max: max
      })
    }
    appStore.showSuccess(t('common.saveSuccess'))
    emit('saved')
  } catch (err: any) {
    appStore.showError(err?.message || t('common.saveFailed'))
  } finally {
    saving.value = false
  }
}
</script>
