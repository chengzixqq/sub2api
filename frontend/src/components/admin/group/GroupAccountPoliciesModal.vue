<template>
  <BaseDialog :show="show" :title="t('admin.groups.accountPoliciesTitle')" width="wide" @close="handleClose">
    <div v-if="group" class="space-y-4">
      <div class="flex flex-wrap items-center gap-2 rounded-lg bg-gray-50 px-3 py-2.5 text-sm dark:bg-dark-700">
        <PlatformIcon :platform="group.platform" size="sm" />
        <span class="font-medium text-gray-900 dark:text-white">{{ group.name }}</span>
      </div>

      <div class="grid gap-5 lg:grid-cols-[minmax(0,14rem)_minmax(0,1fr)]">
        <div class="space-y-3 lg:border-r lg:border-gray-200 lg:pr-5 dark:lg:border-dark-600">
          <div>
            <label class="input-label" for="account-policy-user-search">{{ t('admin.groups.accountPolicyUser') }}</label>
            <Select
              :key="userSelectSession"
              id="account-policy-user-search"
              :model-value="selectedUser?.id ?? null"
              :options="userOptions"
              :disabled="saving"
              :remote="true"
              :searchable="true"
              :loading="loadingUsers"
              :placeholder="t('admin.groups.searchUserPlaceholder')"
              :search-placeholder="t('admin.groups.searchUserPlaceholder')"
              :empty-text="userSearchEmptyText"
              :aria-label="t('admin.groups.accountPolicyUser')"
              @search="searchUsers"
              @change="handleUserSelection"
            >
              <template #option="{ option }">
                <span class="min-w-0 flex-1">
                  <span class="block truncate font-medium">{{ option.user.username || option.user.email }}</span>
                  <span class="block truncate text-xs text-gray-500">{{ option.user.email }}</span>
                </span>
              </template>
            </Select>
          </div>
          <div class="border-t border-gray-200 pt-3 dark:border-dark-600">
            <div class="mb-2 text-xs font-medium uppercase text-gray-500">{{ t('admin.groups.accountPolicies') }} ({{ policies.length }})</div>
            <div v-if="loadingPolicies" class="py-3 text-sm text-gray-500">{{ t('common.loading') }}</div>
            <div v-else-if="!policies.length" class="py-3 text-sm text-gray-500">{{ t('admin.groups.noAccountPolicies') }}</div>
            <button
              v-for="policy in policies"
              :key="policy.user_id"
              type="button"
              class="mb-1 flex w-full items-center justify-between gap-2 rounded-lg px-2.5 py-2 text-left text-sm transition-colors hover:bg-gray-50 disabled:cursor-not-allowed disabled:opacity-60 dark:hover:bg-dark-700"
              :class="selectedUser?.id === policy.user_id ? 'bg-gray-100 dark:bg-dark-700' : ''"
              :disabled="saving"
              @click="requestSelectPolicy(policy)"
            >
              <span class="min-w-0 truncate">{{ policy.username || policy.email || `#${policy.user_id}` }}</span>
              <span class="flex shrink-0 items-center gap-1.5">
                <span v-if="invalidGrantCount(policy)" class="rounded-full bg-red-100 px-1.5 py-0.5 text-[11px] font-medium text-red-700 dark:bg-red-900/40 dark:text-red-300">
                  {{ t('admin.groups.accountPolicyInvalidCount', { count: invalidGrantCount(policy) }) }}
                </span>
                <span class="text-xs text-gray-500">{{ policy.mode === 'append' ? t('admin.groups.accountPolicyAppend') : t('admin.groups.accountPolicyReplace') }}</span>
              </span>
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
              class="rounded-lg px-2 py-1 text-sm text-red-600 transition-colors hover:bg-red-50 disabled:cursor-not-allowed disabled:opacity-50 dark:text-red-400 dark:hover:bg-red-900/20"
              :disabled="saving"
              @click="requestRestoreDefault"
            >{{ t('admin.groups.accountPolicyRestore') }}</button>
          </div>

          <div class="tabs w-full" role="group" :aria-label="t('admin.groups.accountPolicyMode')">
            <button
              v-for="option in modeOptions"
              :key="option.value"
              type="button"
              class="tab min-h-9 flex-1 px-3 py-1.5"
              :class="mode === option.value ? 'tab-active' : ''"
              :aria-pressed="mode === option.value"
              :disabled="saving"
              @click="setMode(option.value)"
            >{{ option.label }}</button>
          </div>

          <div class="border-t border-gray-200 pt-3 dark:border-dark-600">
            <div class="mb-2 flex items-center justify-between gap-2 text-sm font-medium text-gray-700 dark:text-gray-300">
              <span>{{ t('admin.groups.accountPolicySelected') }} ({{ selectedAccounts.length }})</span>
              <span v-if="mode === 'append'" class="text-xs font-normal text-gray-500">{{ t('admin.groups.accountPolicyInherits') }}</span>
            </div>
            <div v-if="selectedAccounts.length" class="flex max-h-28 flex-wrap gap-1.5 overflow-y-auto">
              <div
                v-for="account in selectedAccounts"
                :key="account.account_id"
                class="inline-flex max-w-full items-center gap-1.5 rounded-lg border px-2 py-1 text-xs"
                :class="account.eligible === false
                  ? 'border-red-200 bg-red-50 dark:border-red-800 dark:bg-red-900/20'
                  : 'border-gray-200 bg-white dark:border-dark-600 dark:bg-dark-800'"
              >
                <span class="min-w-0">
                  <span class="flex min-w-0 items-center gap-1.5">
                    <span class="max-w-44 truncate font-medium text-gray-800 dark:text-gray-100">{{ account.account_name || `#${account.account_id}` }}</span>
                    <span class="shrink-0 text-gray-500">{{ account.origin === 'extra' ? t('admin.groups.accountPolicyExtra') : t('admin.groups.accountPolicyMember') }}</span>
                    <span v-if="account.eligible === false" class="shrink-0 rounded-full bg-red-100 px-1.5 py-0.5 font-medium text-red-700 dark:bg-red-900/50 dark:text-red-300">
                      {{ t('admin.groups.accountPolicyIneligible') }}
                    </span>
                  </span>
                  <span v-if="account.eligible === false" class="block max-w-64 truncate text-red-700 dark:text-red-300" :title="ineligibleReason(account)">
                    {{ ineligibleReason(account) }}
                  </span>
                </span>
                <button
                  type="button"
                  class="rounded-md p-0.5 text-gray-500 hover:bg-gray-100 hover:text-gray-900 disabled:cursor-not-allowed disabled:opacity-50 dark:hover:bg-dark-700 dark:hover:text-white"
                  :title="t('common.remove')"
                  :aria-label="t('common.remove')"
                  :disabled="saving"
                  @click="removeAccount(account.account_id)"
                ><Icon name="x" size="xs" /></button>
              </div>
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
              :disabled="saving"
              :placeholder="t('admin.groups.accountPolicySearch')"
              @input="accountPage = 1"
            />
            <div class="max-h-56 overflow-y-auto border-y border-gray-200 dark:border-dark-600">
              <div v-if="loadingAccounts" class="px-2 py-4 text-sm text-gray-500">{{ t('common.loading') }}</div>
              <div v-else-if="!accountRows.length" class="px-2 py-4 text-sm text-gray-500">{{ t('admin.groups.accountPolicyNoAccounts') }}</div>
              <label
                v-for="account in accountRows"
                :key="account.id"
                class="flex cursor-pointer items-center gap-3 rounded-lg px-2 py-2 text-sm transition-colors hover:bg-gray-50 has-[:disabled]:cursor-not-allowed has-[:disabled]:opacity-60 dark:hover:bg-dark-700"
              >
                <input type="checkbox" :checked="isSelected(account.id)" :disabled="saving" @change="toggleAccount(account)" />
                <span class="min-w-0 flex-1">
                  <span class="block truncate">{{ account.name }}</span>
                  <span class="block truncate text-xs text-gray-500" :title="accountGroupNames(account)">
                    {{ t('admin.groups.accountPolicyGroups') }}: {{ accountGroupNames(account) }}
                  </span>
                </span>
                <span v-if="account.dispatch_scope === 'authorized_only'" class="shrink-0 text-xs text-amber-700 dark:text-amber-400">{{ t('admin.groups.accountPolicyPrivate') }}</span>
                <span class="shrink-0 text-xs text-gray-500">{{ accountOrigin(account) === 'member' ? t('admin.groups.accountPolicyMember') : t('admin.groups.accountPolicyExtra') }}</span>
              </label>
            </div>
            <div class="flex items-center justify-end gap-2 text-sm">
              <button type="button" class="rounded-lg p-1.5 transition-colors hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-40 dark:hover:bg-dark-700" :title="t('common.back')" :aria-label="t('common.back')" :disabled="saving || accountPage <= 1" @click="accountPage--"><Icon name="chevronLeft" size="sm" /></button>
              <span>{{ accountPage }} / {{ accountPages }}</span>
              <button type="button" class="rounded-lg p-1.5 transition-colors hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-40 dark:hover:bg-dark-700" :title="t('common.next')" :aria-label="t('common.next')" :disabled="saving || accountPage >= accountPages" @click="accountPage++"><Icon name="chevronRight" size="sm" /></button>
            </div>
          </div>

          <div v-if="confirmEmptyReplace" class="border border-amber-300 bg-amber-50 px-3 py-2 text-sm text-amber-900 dark:border-amber-700 dark:bg-amber-900/20 dark:text-amber-200">
            {{ t('admin.groups.accountPolicyEmptyWarning') }}
          </div>
          <div v-if="hasIneligibleGrants" class="rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-800 dark:border-red-800 dark:bg-red-900/20 dark:text-red-200" role="status">
            {{ t('admin.groups.accountPolicyInvalidGrantsWarning') }}
          </div>
          <div v-if="writeError" class="rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-800 dark:border-red-800 dark:bg-red-900/20 dark:text-red-200" role="alert">
            <div class="font-medium">{{ t('admin.groups.accountPolicySaveFailed') }}</div>
            <div class="mt-1 break-words">{{ writeError }}</div>
          </div>
          <div v-if="isDirty" class="text-xs text-amber-700 dark:text-amber-300" role="status">
            {{ t('admin.groups.accountPolicyUnsavedChanges') }}
          </div>
          <div class="flex justify-end gap-2 border-t border-gray-200 pt-3 dark:border-dark-600">
            <button type="button" class="btn btn-secondary" :disabled="saving" @click="handleClose">{{ t('common.cancel') }}</button>
            <button type="button" class="btn btn-primary" :disabled="saving" @click="savePolicy">
              {{ confirmEmptyReplace ? t('admin.groups.accountPolicyConfirmEmpty') : t('common.save') }}
            </button>
          </div>
        </div>
        <div v-else class="flex min-h-48 items-center justify-center text-sm text-gray-500">{{ t('admin.groups.accountPolicyChooseUser') }}</div>
      </div>
    </div>
  </BaseDialog>
  <ConfirmDialog
    :show="confirmation !== null"
    :title="confirmationTitle"
    :message="confirmationMessage"
    :confirm-text="confirmationConfirmText"
    :cancel-text="t('common.cancel')"
    :danger="true"
    @confirm="confirmPendingAction"
    @cancel="confirmation = null"
  />
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { AccountPolicyMode, AccountPolicySelection, GroupAccountPolicy, GroupAccountPolicyWrite } from '@/api/admin/groups'
import type { AccountListItem, AdminGroup, AdminUser } from '@/types'
import { useAppStore } from '@/stores/app'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import Select, { type SelectOption } from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import { extractApiErrorMessage } from '@/utils/apiError'

const props = defineProps<{ show: boolean; group: AdminGroup | null }>()
const emit = defineEmits<{ close: []; success: [] }>()
const { t } = useI18n()
const appStore = useAppStore()

type UserChoice = Pick<AdminUser, 'id' | 'username' | 'email'>
type UserSelectOption = SelectOption & { user: UserChoice }
type ConfirmationAction =
  | { kind: 'switch-policy'; policy: GroupAccountPolicy }
  | { kind: 'switch-user'; user: UserChoice }
  | { kind: 'close' }
  | { kind: 'restore'; groupID: number; userID: number; version: number; editorSession: number }
type WriteOwner = { groupID: number; userID: number; editorSession: number }
const policies = ref<GroupAccountPolicy[]>([])
const selectedUser = ref<UserChoice | null>(null)
const editingVersion = ref(0)
const mode = ref<AccountPolicyMode>('append')
const selectedAccounts = ref<AccountPolicySelection[]>([])
const userSearch = ref('')
const userResults = ref<UserChoice[]>([])
const loadingUsers = ref(false)
const userSearchError = ref(false)
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
const baselineFingerprint = ref('')
const writeError = ref('')
const confirmation = ref<ConfirmationAction | null>(null)
const editorSession = ref(0)
const userSelectSession = ref(0)
const isDirty = computed(() => Boolean(selectedUser.value) && draftFingerprint() !== baselineFingerprint.value)
const hasIneligibleGrants = computed(() => selectedAccounts.value.some(account => account.eligible === false))
const userOptions = computed<UserSelectOption[]>(() => {
  const users = [...userResults.value]
  if (selectedUser.value && !users.some(user => user.id === selectedUser.value?.id)) {
    users.unshift(selectedUser.value)
  }
  return users.map(user => ({
    value: user.id,
    label: user.username || user.email,
    user
  }))
})
const userSearchEmptyText = computed(() => {
  if (userSearchError.value) return t('admin.groups.accountPolicyUserSearchFailed')
  if (userSearch.value.trim().length < 2) return t('admin.groups.accountPolicySearchMinimum')
  return t('admin.groups.accountPolicyNoUsers')
})
const modeOptions = computed(() => [
  { value: 'append' as const, label: t('admin.groups.accountPolicyAppend') },
  { value: 'replace' as const, label: t('admin.groups.accountPolicyReplace') }
])
const confirmationTitle = computed(() => confirmation.value?.kind === 'restore'
  ? t('admin.groups.accountPolicyRestoreConfirmTitle')
  : t('admin.groups.accountPolicyDiscardConfirmTitle'))
const confirmationMessage = computed(() => confirmation.value?.kind === 'restore'
  ? t('admin.groups.accountPolicyRestoreConfirmMessage')
  : t('admin.groups.accountPolicyDiscardConfirmMessage'))
const confirmationConfirmText = computed(() => confirmation.value?.kind === 'restore'
  ? t('admin.groups.accountPolicyRestore')
  : t('admin.groups.accountPolicyDiscardConfirm'))

let policyRequest = 0
let accountRequest = 0
let userRequest = 0
let groupNamesRequest = 0
let accountTimer: ReturnType<typeof setTimeout> | undefined

const draftFingerprint = () => JSON.stringify({
  userID: selectedUser.value?.id ?? null,
  mode: mode.value,
  accounts: selectedAccounts.value
    .map(({ account_id, origin }) => ({ account_id, origin }))
    .sort((a, b) => a.account_id - b.account_id)
})

const setDraftBaseline = () => {
  baselineFingerprint.value = draftFingerprint()
}

const clearDraftError = () => {
  writeError.value = ''
}

const invalidGrantCount = (policy: GroupAccountPolicy) => policy.accounts.filter(account => account.eligible === false).length

const ineligibleReason = (account: AccountPolicySelection) => {
  const reason = account.ineligible_reason?.trim()
  if (!reason) return t('admin.groups.accountPolicyUnknownIneligibleReason')
  const key = `admin.groups.accountPolicyIneligibleReasons.${reason}`
  const translated = t(key)
  return translated === key ? reason : translated
}

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

const applyPolicy = (policy: GroupAccountPolicy) => {
  editorSession.value++
  selectedUser.value = { id: policy.user_id, username: policy.username, email: policy.email }
  editingVersion.value = policy.version
  mode.value = policy.mode
  selectedAccounts.value = policy.accounts.map(account => ({ ...account }))
  confirmEmptyReplace.value = false
  userResults.value = []
  userSearch.value = ''
  writeError.value = ''
  setDraftBaseline()
}

const applyUser = (user: UserChoice) => {
  const existing = policies.value.find(policy => policy.user_id === user.id)
  if (existing) {
    applyPolicy(existing)
    return
  }
  editorSession.value++
  selectedUser.value = { ...user }
  editingVersion.value = 0
  mode.value = 'append'
  selectedAccounts.value = []
  confirmEmptyReplace.value = false
  userResults.value = []
  userSearch.value = ''
  writeError.value = ''
  setDraftBaseline()
}

const requestAction = (action: ConfirmationAction) => {
  if (saving.value) return
  if (action.kind === 'restore' || isDirty.value) {
    confirmation.value = action
    return
  }
  runConfirmedAction(action)
}

const requestSelectPolicy = (policy: GroupAccountPolicy) => {
  if (selectedUser.value?.id === policy.user_id) return
  requestAction({ kind: 'switch-policy', policy })
}

const requestSelectUser = (user: UserChoice) => {
  if (selectedUser.value?.id === user.id) return
  requestAction({ kind: 'switch-user', user })
}

const handleUserSelection = (_value: string | number | boolean | null, option: SelectOption | null) => {
  const user = (option as UserSelectOption | null)?.user
  if (user) requestSelectUser(user)
}

const runConfirmedAction = (action: ConfirmationAction) => {
  if (action.kind === 'switch-policy') applyPolicy(action.policy)
  else if (action.kind === 'switch-user') applyUser(action.user)
  else if (action.kind === 'close') emit('close')
  else void restoreDefault(action)
}

const confirmPendingAction = () => {
  const action = confirmation.value
  confirmation.value = null
  if (action) runConfirmedAction(action)
}

const setMode = (value: AccountPolicyMode) => {
  if (saving.value) return
  mode.value = value
  confirmEmptyReplace.value = false
  clearDraftError()
}

const isSelected = (id: number) => selectedAccounts.value.some(account => account.account_id === id)

const accountOrigin = (account: AccountListItem): AccountPolicySelection['origin'] => {
  const selection = selectedAccounts.value.find(item => item.account_id === account.id)
  return selection?.origin ?? (account.group_ids?.includes(props.group?.id ?? -1) ? 'member' : 'extra')
}

const toggleAccount = (account: AccountListItem) => {
  if (!props.group || saving.value) return
  if (isSelected(account.id)) selectedAccounts.value = selectedAccounts.value.filter(item => item.account_id !== account.id)
  else selectedAccounts.value = [
    ...selectedAccounts.value,
    {
      account_id: account.id,
      account_name: account.name,
      dispatch_scope: account.dispatch_scope,
      origin: account.group_ids?.includes(props.group.id) ? 'member' : 'extra',
      eligible: true
    }
  ]
  confirmEmptyReplace.value = false
  clearDraftError()
}

const removeAccount = (id: number) => {
  if (saving.value) return
  selectedAccounts.value = selectedAccounts.value.filter(account => account.account_id !== id)
  confirmEmptyReplace.value = false
  clearDraftError()
}

const ownerIsCurrent = (owner: WriteOwner) =>
  props.show && props.group?.id === owner.groupID && selectedUser.value?.id === owner.userID && editorSession.value === owner.editorSession

const groupIsCurrent = (owner: WriteOwner) => props.show && props.group?.id === owner.groupID

const handleWriteFailure = async (error: unknown, owner: WriteOwner) => {
  const failure = error as { status?: number; response?: { status?: number } } | null
  const conflict = (failure?.status ?? failure?.response?.status) === 412
  if (conflict) {
    appStore.showError(t('admin.groups.accountPolicyConflict'))
    if (ownerIsCurrent(owner)) {
      editorSession.value++
      selectedUser.value = null
      editingVersion.value = 0
      selectedAccounts.value = []
      confirmEmptyReplace.value = false
      setDraftBaseline()
      clearDraftError()
    }
  } else {
    const message = extractApiErrorMessage(error, t('admin.groups.accountPolicySaveFailed'))
    appStore.showError(message)
    if (ownerIsCurrent(owner)) writeError.value = message
  }
  if (groupIsCurrent(owner)) await loadPolicies()
}

const savePolicy = async () => {
  if (!props.group || !selectedUser.value || saving.value) return
  if (mode.value === 'replace' && selectedAccounts.value.length === 0 && !confirmEmptyReplace.value) {
    confirmEmptyReplace.value = true
    return
  }
  const owner: WriteOwner = {
    groupID: props.group.id,
    userID: selectedUser.value.id,
    editorSession: editorSession.value
  }
  const version = editingVersion.value
  const payload: GroupAccountPolicyWrite = {
    mode: mode.value,
    accounts: selectedAccounts.value.map(({ account_id, origin }) => ({ account_id, origin }))
  }
  policyRequest++
  loadingPolicies.value = false
  saving.value = true
  try {
    const policy = await adminAPI.groups.putGroupAccountPolicy(owner.groupID, owner.userID, version, payload)
    if (groupIsCurrent(owner)) {
      policies.value = [...policies.value.filter(entry => entry.user_id !== owner.userID), policy]
    }
    if (ownerIsCurrent(owner)) {
      editingVersion.value = policy.version
      mode.value = policy.mode
      selectedAccounts.value = policy.accounts.map(account => ({ ...account }))
      confirmEmptyReplace.value = false
      clearDraftError()
      setDraftBaseline()
    }
    appStore.showSuccess(t('admin.groups.accountPolicySaved'))
    emit('success')
  } catch (error) {
    await handleWriteFailure(error, owner)
  } finally {
    saving.value = false
  }
}

const requestRestoreDefault = () => {
  if (!props.group || !selectedUser.value || !activePolicy.value || saving.value) return
  requestAction({
    kind: 'restore',
    groupID: props.group.id,
    userID: selectedUser.value.id,
    version: editingVersion.value,
    editorSession: editorSession.value
  })
}

const restoreDefault = async (owner: WriteOwner & { version: number }) => {
  if (saving.value || !ownerIsCurrent(owner) || !activePolicy.value) return
  policyRequest++
  loadingPolicies.value = false
  saving.value = true
  try {
    await adminAPI.groups.deleteGroupAccountPolicy(owner.groupID, owner.userID, owner.version)
    if (groupIsCurrent(owner)) policies.value = policies.value.filter(entry => entry.user_id !== owner.userID)
    if (ownerIsCurrent(owner)) {
      editingVersion.value = 0
      mode.value = 'append'
      selectedAccounts.value = []
      confirmEmptyReplace.value = false
      clearDraftError()
      editorSession.value++
      setDraftBaseline()
    }
    appStore.showSuccess(t('admin.groups.accountPolicyRestored'))
    emit('success')
  } catch (error) {
    await handleWriteFailure(error, owner)
  } finally {
    saving.value = false
  }
}

const handleClose = () => {
  if (saving.value || confirmation.value) return
  if (isDirty.value) confirmation.value = { kind: 'close' }
  else emit('close')
}

const searchUsers = async (query: string) => {
  userSearch.value = query
  const request = ++userRequest
  userResults.value = []
  userSearchError.value = false
  if (!props.show || query.trim().length < 2) {
    loadingUsers.value = false
    return
  }
  loadingUsers.value = true
  try {
    const result = await adminAPI.users.list(1, 10, { search: query.trim() })
    if (request === userRequest) userResults.value = result.items.map(({ id, username, email }) => ({ id, username, email }))
  } catch (error) {
    if (request === userRequest) {
      userSearchError.value = true
      appStore.showError(extractApiErrorMessage(error, t('admin.groups.accountPolicyUserSearchFailed')))
    }
  } finally {
    if (request === userRequest) loadingUsers.value = false
  }
}

watch([() => props.show, () => props.group?.id], ([show]) => {
  policyRequest++
  accountRequest++
  userRequest++
  groupNamesRequest++
  clearTimeout(accountTimer)
  editorSession.value++
  userSelectSession.value++
  policies.value = []
  groupNames.value = new Map(props.group ? [[props.group.id, props.group.name]] : [])
  selectedUser.value = null
  editingVersion.value = 0
  userSearch.value = ''
  userResults.value = []
  loadingUsers.value = false
  userSearchError.value = false
  accountSearch.value = ''
  accountPage.value = 1
  accountRows.value = []
  selectedAccounts.value = []
  confirmEmptyReplace.value = false
  confirmation.value = null
  writeError.value = ''
  baselineFingerprint.value = draftFingerprint()
  loadingPolicies.value = false
  loadingAccounts.value = false
  if (show && props.group) {
    void loadPolicies()
    void loadGroupNames()
  }
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
  clearTimeout(accountTimer)
})
</script>
