<template>
  <BaseDialog :show="show" :title="t('admin.groups.accountPoliciesTitle')" width="wide" @close="handleClose">
    <div v-if="group" class="space-y-4">
      <div class="flex items-center gap-2 border-b border-gray-200 pb-3 text-sm dark:border-dark-600">
        <PlatformIcon :platform="group.platform" size="sm" />
        <span class="font-medium text-gray-900 dark:text-white">{{ group.name }}</span>
      </div>

      <div class="grid gap-5 lg:grid-cols-[minmax(0,14rem)_minmax(0,1fr)]">
        <div class="space-y-3 lg:border-r lg:border-gray-200 lg:pr-5 dark:lg:border-dark-600">
          <div class="relative">
            <label class="input-label" for="account-policy-user-search">{{ t('admin.groups.accountPolicyUser') }}</label>
            <input
              id="account-policy-user-search"
              v-model="userSearch"
              class="input w-full"
              autocomplete="off"
              :placeholder="t('admin.groups.searchUserPlaceholder')"
            />
            <div
              v-if="userResults.length"
              class="absolute z-20 mt-1 max-h-48 w-full overflow-y-auto border border-gray-200 bg-white shadow-lg dark:border-dark-600 dark:bg-dark-700"
            >
              <button
                v-for="user in userResults"
                :key="user.id"
                type="button"
                class="block w-full px-3 py-2 text-left text-sm hover:bg-gray-50 dark:hover:bg-dark-600"
                @click="selectUser(user)"
              >
                <span class="block truncate font-medium">{{ user.username || user.email }}</span>
                <span class="block truncate text-xs text-gray-500">{{ user.email }}</span>
              </button>
            </div>
          </div>
          <div class="border-t border-gray-200 pt-3 dark:border-dark-600">
            <div class="mb-2 text-xs font-medium uppercase text-gray-500">{{ t('admin.groups.accountPolicies') }} ({{ policies.length }})</div>
            <div v-if="loadingPolicies" class="py-3 text-sm text-gray-500">{{ t('common.loading') }}</div>
            <div v-else-if="!policies.length" class="py-3 text-sm text-gray-500">{{ t('admin.groups.noAccountPolicies') }}</div>
            <button
              v-for="policy in policies"
              :key="policy.user_id"
              type="button"
              class="mb-1 flex w-full items-center justify-between gap-2 px-2 py-2 text-left text-sm hover:bg-gray-50 dark:hover:bg-dark-700"
              :class="selectedUser?.id === policy.user_id ? 'bg-gray-100 dark:bg-dark-700' : ''"
              @click="selectPolicy(policy)"
            >
              <span class="min-w-0 truncate">{{ policy.username || policy.email || `#${policy.user_id}` }}</span>
              <span class="shrink-0 text-xs text-gray-500">{{ policy.mode === 'append' ? t('admin.groups.accountPolicyAppend') : t('admin.groups.accountPolicyReplace') }}</span>
            </button>
          </div>
        </div>

        <div v-if="selectedUser" class="min-w-0 space-y-4">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <div class="min-w-0">
              <div class="truncate text-sm font-semibold text-gray-900 dark:text-white">{{ selectedUser.username || selectedUser.email }}</div>
              <div class="truncate text-xs text-gray-500">{{ selectedUser.email }}</div>
            </div>
            <button
              v-if="activePolicy"
              type="button"
              class="text-sm text-red-600 hover:underline disabled:opacity-50"
              :disabled="saving"
              @click="restoreDefault"
            >{{ t('admin.groups.accountPolicyRestore') }}</button>
          </div>

          <div class="inline-flex border border-gray-200 dark:border-dark-600" role="group" :aria-label="t('admin.groups.accountPolicyMode')">
            <button
              v-for="option in modeOptions"
              :key="option.value"
              type="button"
              class="px-3 py-1.5 text-sm"
              :class="mode === option.value ? 'bg-primary-600 text-white' : 'text-gray-700 hover:bg-gray-50 dark:text-gray-300 dark:hover:bg-dark-700'"
              :aria-pressed="mode === option.value"
              @click="setMode(option.value)"
            >{{ option.label }}</button>
          </div>

          <div class="border-t border-gray-200 pt-3 dark:border-dark-600">
            <div class="mb-2 flex items-center justify-between gap-2 text-sm font-medium text-gray-700 dark:text-gray-300">
              <span>{{ t('admin.groups.accountPolicySelected') }} ({{ selectedAccounts.length }})</span>
              <span v-if="mode === 'append'" class="text-xs font-normal text-gray-500">{{ t('admin.groups.accountPolicyInherits') }}</span>
            </div>
            <div v-if="selectedAccounts.length" class="flex max-h-28 flex-wrap gap-1.5 overflow-y-auto">
              <span v-for="account in selectedAccounts" :key="account.account_id" class="inline-flex max-w-full items-center gap-1 border border-gray-200 px-2 py-1 text-xs dark:border-dark-600">
                <span class="truncate">{{ account.account_name || `#${account.account_id}` }}</span>
                <span class="shrink-0 text-gray-500">{{ account.origin === 'extra' ? t('admin.groups.accountPolicyExtra') : t('admin.groups.accountPolicyMember') }}</span>
                <button type="button" :title="t('common.remove')" :aria-label="t('common.remove')" @click="removeAccount(account.account_id)"><Icon name="x" size="xs" /></button>
              </span>
            </div>
            <div v-else class="text-sm text-gray-500">{{ t('admin.groups.accountPolicyNoneSelected') }}</div>
          </div>

          <div class="space-y-2 border-t border-gray-200 pt-3 dark:border-dark-600">
            <label class="input-label" for="account-policy-account-search">{{ t('admin.groups.accountPolicyAccount') }}</label>
            <input
              id="account-policy-account-search"
              v-model="accountSearch"
              class="input w-full"
              autocomplete="off"
              :placeholder="t('admin.groups.accountPolicySearch')"
              @input="accountPage = 1"
            />
            <div class="max-h-56 overflow-y-auto border-y border-gray-200 dark:border-dark-600">
              <div v-if="loadingAccounts" class="px-2 py-4 text-sm text-gray-500">{{ t('common.loading') }}</div>
              <div v-else-if="!accountRows.length" class="px-2 py-4 text-sm text-gray-500">{{ t('admin.groups.accountPolicyNoAccounts') }}</div>
              <label
                v-for="account in accountRows"
                :key="account.id"
                class="flex cursor-pointer items-center gap-3 border-b border-gray-100 px-2 py-2 text-sm last:border-0 hover:bg-gray-50 dark:border-dark-700 dark:hover:bg-dark-700"
              >
                <input type="checkbox" :checked="isSelected(account.id)" @change="toggleAccount(account)" />
                <span class="min-w-0 flex-1">
                  <span class="block truncate">{{ account.name }}</span>
                  <span class="block truncate text-xs text-gray-500" :title="accountGroupNames(account)">
                    {{ t('admin.groups.accountPolicyGroups') }}: {{ accountGroupNames(account) }}
                  </span>
                </span>
                <span v-if="account.dispatch_scope === 'authorized_only'" class="shrink-0 text-xs text-amber-700 dark:text-amber-400">{{ t('admin.groups.accountPolicyPrivate') }}</span>
                <span class="shrink-0 text-xs text-gray-500">{{ account.group_ids?.includes(group.id) ? t('admin.groups.accountPolicyMember') : t('admin.groups.accountPolicyExtra') }}</span>
              </label>
            </div>
            <div class="flex items-center justify-end gap-2 text-sm">
              <button type="button" class="p-1 disabled:opacity-40" :title="t('common.back')" :aria-label="t('common.back')" :disabled="accountPage <= 1" @click="accountPage--"><Icon name="chevronLeft" size="sm" /></button>
              <span>{{ accountPage }} / {{ accountPages }}</span>
              <button type="button" class="p-1 disabled:opacity-40" :title="t('common.next')" :aria-label="t('common.next')" :disabled="accountPage >= accountPages" @click="accountPage++"><Icon name="chevronRight" size="sm" /></button>
            </div>
          </div>

          <div v-if="confirmEmptyReplace" class="border border-amber-300 bg-amber-50 px-3 py-2 text-sm text-amber-900 dark:border-amber-700 dark:bg-amber-900/20 dark:text-amber-200">
            {{ t('admin.groups.accountPolicyEmptyWarning') }}
          </div>
          <div class="flex justify-end gap-2 border-t border-gray-200 pt-3 dark:border-dark-600">
            <button type="button" class="btn btn-secondary" @click="handleClose">{{ t('common.cancel') }}</button>
            <button type="button" class="btn btn-primary" :disabled="saving" @click="savePolicy">
              {{ confirmEmptyReplace ? t('admin.groups.accountPolicyConfirmEmpty') : t('common.save') }}
            </button>
          </div>
        </div>
        <div v-else class="flex min-h-48 items-center justify-center text-sm text-gray-500">{{ t('admin.groups.accountPolicyChooseUser') }}</div>
      </div>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { AccountPolicyMode, AccountPolicySelection, GroupAccountPolicy } from '@/api/admin/groups'
import type { AccountListItem, AdminGroup, AdminUser } from '@/types'
import { useAppStore } from '@/stores/app'
import BaseDialog from '@/components/common/BaseDialog.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import Icon from '@/components/icons/Icon.vue'

const props = defineProps<{ show: boolean; group: AdminGroup | null }>()
const emit = defineEmits<{ close: []; success: [] }>()
const { t } = useI18n()
const appStore = useAppStore()

type UserChoice = Pick<AdminUser, 'id' | 'username' | 'email'>
const policies = ref<GroupAccountPolicy[]>([])
const selectedUser = ref<UserChoice | null>(null)
const editingVersion = ref(0)
const mode = ref<AccountPolicyMode>('append')
const selectedAccounts = ref<AccountPolicySelection[]>([])
const userSearch = ref('')
const userResults = ref<AdminUser[]>([])
const accountSearch = ref('')
const accountRows = ref<AccountListItem[]>([])
const groupNames = ref(new Map<number, string>())
const accountPage = ref(1)
const accountPages = ref(1)
const loadingPolicies = ref(false)
const loadingAccounts = ref(false)
const saving = ref(false)
const confirmEmptyReplace = ref(false)
const activePolicy = computed(() => policies.value.find(p => p.user_id === selectedUser.value?.id))
const modeOptions = computed(() => [
  { value: 'append' as const, label: t('admin.groups.accountPolicyAppend') },
  { value: 'replace' as const, label: t('admin.groups.accountPolicyReplace') }
])

let policyRequest = 0
let accountRequest = 0
let userRequest = 0
let groupNamesRequest = 0
let userTimer: ReturnType<typeof setTimeout> | undefined
let accountTimer: ReturnType<typeof setTimeout> | undefined

const loadPolicies = async () => {
  const groupID = props.group?.id
  if (!groupID) return
  const request = ++policyRequest
  loadingPolicies.value = true
  try {
    const entries = await adminAPI.groups.getGroupAccountPolicies(groupID)
    if (request === policyRequest) policies.value = entries
  } catch (error) {
    if (request === policyRequest) appStore.showError(t('admin.groups.failedToLoad'))
  } finally {
    if (request === policyRequest) loadingPolicies.value = false
  }
}

const loadGroupNames = async () => {
  const request = ++groupNamesRequest
  try {
    const groups = await adminAPI.groups.getAllIncludingInactive()
    if (request === groupNamesRequest) groupNames.value = new Map(groups.map(group => [group.id, group.name]))
  } catch {
    // Group IDs remain visible when the directory cannot be read.
    if (request === groupNamesRequest) appStore.showError(t('admin.groups.failedToLoad'))
  }
}

const accountGroupNames = (account: AccountListItem) => {
  const groupIDs = account.group_ids ?? []
  if (groupIDs.length === 0) return t('admin.groups.accountPolicyUngrouped')
  return groupIDs.map(id => groupNames.value.get(id) || `#${id}`).join(', ')
}

const loadAccounts = async () => {
  const group = props.group
  if (!props.show || !group || !selectedUser.value) return
  const request = ++accountRequest
  loadingAccounts.value = true
  try {
    const filters: { platform?: string; group?: string; search?: string; lite: string } = { lite: '1' }
    if (group.platform === 'composite') filters.group = String(group.id)
    else filters.platform = group.platform
    if (accountSearch.value.trim()) filters.search = accountSearch.value.trim()
    const result = await adminAPI.accounts.list(accountPage.value, 10, filters)
    if (request !== accountRequest) return
    accountRows.value = result.items
    accountPages.value = Math.max(1, result.pages)
  } catch (error) {
    if (request === accountRequest) appStore.showError(t('admin.groups.failedToLoad'))
  } finally {
    if (request === accountRequest) loadingAccounts.value = false
  }
}

const selectPolicy = (policy: GroupAccountPolicy) => {
  selectedUser.value = { id: policy.user_id, username: policy.username, email: policy.email }
  editingVersion.value = policy.version
  mode.value = policy.mode
  selectedAccounts.value = policy.accounts.map(account => ({ ...account }))
  confirmEmptyReplace.value = false
  userResults.value = []
  userSearch.value = ''
}

const selectUser = (user: AdminUser) => {
  const existing = policies.value.find(policy => policy.user_id === user.id)
  if (existing) selectPolicy(existing)
  else {
    selectedUser.value = { id: user.id, username: user.username, email: user.email }
    editingVersion.value = 0
    mode.value = 'append'
    selectedAccounts.value = []
    confirmEmptyReplace.value = false
    userResults.value = []
    userSearch.value = ''
  }
}

const setMode = (value: AccountPolicyMode) => {
  mode.value = value
  confirmEmptyReplace.value = false
}

const isSelected = (id: number) => selectedAccounts.value.some(account => account.account_id === id)

const toggleAccount = (account: AccountListItem) => {
  if (!props.group) return
  if (isSelected(account.id)) selectedAccounts.value = selectedAccounts.value.filter(item => item.account_id !== account.id)
  else selectedAccounts.value = [
    ...selectedAccounts.value,
    {
      account_id: account.id,
      account_name: account.name,
      dispatch_scope: account.dispatch_scope,
      origin: account.group_ids?.includes(props.group.id) ? 'member' : 'extra'
    }
  ]
  confirmEmptyReplace.value = false
}

const removeAccount = (id: number) => {
  selectedAccounts.value = selectedAccounts.value.filter(account => account.account_id !== id)
  confirmEmptyReplace.value = false
}

const handleWriteFailure = async (error: unknown, groupID: number, userID: number) => {
  const failure = error as { status?: number; response?: { status?: number } } | null
  const conflict = (failure?.status ?? failure?.response?.status) === 412
  appStore.showError(t(conflict ? 'admin.groups.accountPolicyConflict' : 'admin.groups.failedToSave'))
  if (!props.show || props.group?.id !== groupID) return
  if (conflict && selectedUser.value?.id === userID) {
    // Require a fresh selection so a refreshed version can never accompany a stale draft.
    selectedUser.value = null
    editingVersion.value = 0
    selectedAccounts.value = []
    confirmEmptyReplace.value = false
  }
  await loadPolicies()
}

const savePolicy = async () => {
  if (!props.group || !selectedUser.value || saving.value) return
  if (mode.value === 'replace' && selectedAccounts.value.length === 0 && !confirmEmptyReplace.value) {
    confirmEmptyReplace.value = true
    return
  }
  const groupID = props.group.id
  const userID = selectedUser.value.id
  const version = editingVersion.value
  const nextMode = mode.value
  const accounts = selectedAccounts.value.map(({ account_id, origin }) => ({ account_id, origin }))
  saving.value = true
  try {
    const policy = await adminAPI.groups.putGroupAccountPolicy(
      groupID,
      userID,
      version,
      { mode: nextMode, accounts }
    )
    if (props.show && props.group?.id === groupID && selectedUser.value?.id === userID) {
      policies.value = [...policies.value.filter(entry => entry.user_id !== policy.user_id), policy]
      selectPolicy(policy)
    }
    appStore.showSuccess(t('admin.groups.accountPolicySaved'))
    emit('success')
  } catch (error) {
    await handleWriteFailure(error, groupID, userID)
  } finally {
    saving.value = false
  }
}

const restoreDefault = async () => {
  if (!props.group || !selectedUser.value || !activePolicy.value || saving.value) return
  const groupID = props.group.id
  const userID = selectedUser.value.id
  const version = editingVersion.value
  saving.value = true
  try {
    await adminAPI.groups.deleteGroupAccountPolicy(groupID, userID, version)
    if (props.show && props.group?.id === groupID && selectedUser.value?.id === userID) {
      policies.value = policies.value.filter(entry => entry.user_id !== userID)
      editingVersion.value = 0
      mode.value = 'append'
      selectedAccounts.value = []
      confirmEmptyReplace.value = false
    }
    appStore.showSuccess(t('admin.groups.accountPolicyRestored'))
    emit('success')
  } catch (error) {
    await handleWriteFailure(error, groupID, userID)
  } finally {
    saving.value = false
  }
}

const handleClose = () => emit('close')

watch([() => props.show, () => props.group?.id], ([show]) => {
  policyRequest++
  accountRequest++
  userRequest++
  groupNamesRequest++
  clearTimeout(userTimer)
  clearTimeout(accountTimer)
  policies.value = []
  groupNames.value = new Map(props.group ? [[props.group.id, props.group.name]] : [])
  selectedUser.value = null
  editingVersion.value = 0
  userSearch.value = ''
  userResults.value = []
  accountSearch.value = ''
  accountPage.value = 1
  accountRows.value = []
  selectedAccounts.value = []
  confirmEmptyReplace.value = false
  if (show && props.group) {
    void loadPolicies()
    void loadGroupNames()
  }
})

watch(userSearch, query => {
  clearTimeout(userTimer)
  const request = ++userRequest
  userResults.value = []
  if (!props.show || query.trim().length < 2) return
  userTimer = setTimeout(async () => {
    try {
      const result = await adminAPI.users.list(1, 10, { search: query.trim() })
      if (request === userRequest) userResults.value = result.items
    } catch {
      if (request === userRequest) userResults.value = []
    }
  }, 250)
})

watch([accountSearch, accountPage, selectedUser], () => {
  clearTimeout(accountTimer)
  ++accountRequest
  accountRows.value = []
  if (!selectedUser.value || !props.show) return
  accountTimer = setTimeout(() => void loadAccounts(), 200)
})

onUnmounted(() => {
  policyRequest++
  accountRequest++
  userRequest++
  groupNamesRequest++
  clearTimeout(userTimer)
  clearTimeout(accountTimer)
})
</script>
