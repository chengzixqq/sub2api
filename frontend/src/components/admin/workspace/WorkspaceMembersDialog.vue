<template>
  <BaseDialog
    :show="true"
    :title="t('admin.workspaces.membersTitle', { name: workspace.name })"
    width="normal"
    @close="emit('close')"
  >
    <div class="space-y-4">
      <!-- 绑定普通用户会连带改其全局角色，把这件事写在入口处，
           而不是等提升发生后才在 toast 里一闪而过。 -->
      <p class="rounded-lg bg-blue-50 px-3 py-2 text-xs leading-relaxed text-blue-800 dark:bg-blue-900/20 dark:text-blue-300">
        {{ t('admin.workspaces.memberSearchHint') }}
      </p>

      <!-- 绑定入口：搜索候选人后点选。
           一个用户同时只能属于一个工作区，后端在冲突时返回 409。 -->
      <div class="relative">
        <input
          v-model="query"
          type="text"
          class="input"
          :placeholder="t('admin.workspaces.memberSearchPlaceholder')"
          @focus="searchOpen = true"
        />

        <div
          v-if="searchOpen && query.trim()"
          class="absolute z-20 mt-1 max-h-60 w-full overflow-y-auto rounded-lg border border-gray-200 bg-white shadow-lg dark:border-dark-600 dark:bg-dark-800"
        >
          <div v-if="searching" class="px-3 py-2.5 text-sm text-gray-500">
            {{ t('common.loading') }}
          </div>
          <div v-else-if="!candidates.length" class="px-3 py-2.5 text-sm text-gray-500">
            {{ t('admin.workspaces.memberSearchEmpty') }}
          </div>
          <button
            v-for="u in candidates"
            v-else
            :key="u.id"
            type="button"
            :data-testid="`member-candidate-${u.id}`"
            class="flex w-full items-center justify-between gap-2 px-3 py-2 text-left transition-colors hover:bg-gray-50 disabled:cursor-not-allowed disabled:opacity-50 dark:hover:bg-dark-700"
            :disabled="adding"
            @click="add(u)"
          >
            <span class="min-w-0">
              <span class="block truncate text-sm text-gray-900 dark:text-gray-100">
                {{ u.username || u.email }}
              </span>
              <span class="block truncate text-xs text-gray-500 dark:text-gray-400">
                {{ u.email }} · #{{ u.id }}
              </span>
            </span>
            <!-- 普通用户会被顺带提升为供应商，绑定前就标出来，
                 避免站长事后发现别人的全局角色被改了 -->
            <span
              v-if="u.role !== 'vendor'"
              class="whitespace-nowrap rounded bg-amber-50 px-1.5 py-0.5 text-xs text-amber-700 dark:bg-amber-900/30 dark:text-amber-300"
            >
              {{ t('admin.workspaces.memberWillPromote') }}
            </span>
            <span v-else class="whitespace-nowrap text-xs text-primary-600 dark:text-primary-400">
              {{ t('admin.workspaces.addMember') }}
            </span>
          </button>
        </div>
      </div>

      <!-- 放在 relative 容器之外：容器内会被绝对定位的候选下拉遮住 -->
      <label class="flex w-fit cursor-pointer items-center gap-2 text-xs text-gray-600 dark:text-gray-400">
        <input
          v-model="vendorOnly"
          type="checkbox"
          class="h-3.5 w-3.5 rounded border-gray-300 text-primary-600"
        />
        {{ t('admin.workspaces.memberSearchVendorOnly') }}
      </label>

      <div v-if="loading" class="py-6 text-center text-sm text-gray-500">
        {{ t('common.loading') }}
      </div>
      <div v-else-if="!members.length" class="py-6 text-center text-sm text-gray-500">
        {{ t('admin.workspaces.noMembers') }}
      </div>
      <ul v-else class="divide-y divide-gray-100 dark:divide-dark-700">
        <li v-for="m in members" :key="m.id" class="flex items-center justify-between gap-3 py-2.5">
          <div class="min-w-0">
            <div class="truncate text-sm text-gray-900 dark:text-gray-100">
              {{ m.username || m.email || t('admin.workspaces.userIdLabel', { id: m.user_id }) }}
            </div>
            <div class="truncate text-xs text-gray-500 dark:text-gray-400">
              {{ m.email || t('admin.workspaces.userIdLabel', { id: m.user_id }) }}
              <!-- 角色已不是 vendor 的绑定形同虚设：对方进不了管理端。
                   标出来让站长知道要去用户管理改角色，而不是以为绑定坏了。 -->
              <span v-if="m.role && m.role !== 'vendor'" class="ml-1 text-amber-600 dark:text-amber-400">
                · {{ t('admin.workspaces.memberRoleMismatch') }}
              </span>
            </div>
          </div>
          <button class="whitespace-nowrap text-sm text-red-600 hover:underline" @click="remove(m.user_id)">
            {{ t('admin.workspaces.removeMember') }}
          </button>
        </li>
      </ul>
    </div>

    <template #footer>
      <button class="btn btn-secondary" @click="emit('close')">{{ t('common.close') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { onMounted, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import {
  adminWorkspacesAPI,
  type Workspace,
  type WorkspaceMember
} from '@/api/admin/workspaces'
import usersAPI from '@/api/admin/users'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { useAppStore } from '@/stores/app'

const props = defineProps<{ workspace: Workspace }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const { t } = useI18n()
const appStore = useAppStore()

const members = ref<WorkspaceMember[]>([])
const loading = ref(false)
const adding = ref(false)

const query = ref('')
const searchOpen = ref(false)
const searching = ref(false)
// 默认只列供应商：多数时候要绑的就是已有供应商账号，
// 取消勾选才把普通用户纳入候选（绑定时自动提升角色）。
const vendorOnly = ref(true)
const candidates = ref<Array<{ id: number; email: string; username: string; role: string }>>([])

// 每次搜索都带上一轮的取消器：输入快于响应时，
// 迟到的旧响应会覆盖新结果，导致候选列表与输入框不一致。
let searchController: AbortController | null = null
let searchTimer: ReturnType<typeof setTimeout> | null = null

async function reload() {
  loading.value = true
  try {
    members.value = await adminWorkspacesAPI.listMembers(props.workspace.id)
  } catch (err: any) {
    appStore.showError(err?.message || t('admin.workspaces.loadFailed'))
  } finally {
    loading.value = false
  }
}

/**
 * 搜索候选用户。
 *
 * 默认只列供应商（vendorOnly），取消勾选可搜出普通用户 ——
 * 绑定普通用户时后端会顺带把角色提升为 vendor，不需要站长
 * 先去用户管理改一遍再回来重选。
 *
 * admin 一律不进候选：降级站长不可逆，还可能降掉最后一个 admin
 * 而锁死后台，后端也会拒。放进候选只会让人白点一次。
 */
async function runSearch(keyword: string) {
  searchController?.abort()
  const controller = new AbortController()
  searchController = controller

  searching.value = true
  try {
    const res = await usersAPI.list(
      1,
      10,
      // vendorOnly 关闭时不传 role，让普通用户也能被搜到。
      { search: keyword, ...(vendorOnly.value ? { role: 'vendor' as const } : {}) },
      { signal: controller.signal }
    )
    // 已在本工作区的成员不再出现在候选里。
    const bound = new Set(members.value.map((m) => m.user_id))
    candidates.value = res.items
      .filter((u) => !bound.has(u.id) && u.role !== 'admin')
      .map((u) => ({
        id: u.id,
        email: u.email,
        username: u.username ?? '',
        role: u.role ?? ''
      }))
  } catch (err: any) {
    // 取消属于正常流程（用户又敲了一个字），不打扰用户。
    if (controller.signal.aborted) return
    candidates.value = []
    appStore.showError(err?.message || t('admin.workspaces.loadFailed'))
  } finally {
    if (!controller.signal.aborted) {
      searching.value = false
    }
  }
}

watch(query, (value) => {
  if (searchTimer) clearTimeout(searchTimer)
  const keyword = value.trim()
  if (!keyword) {
    searchController?.abort()
    candidates.value = []
    searching.value = false
    return
  }
  searchOpen.value = true
  searching.value = true
  searchTimer = setTimeout(() => runSearch(keyword), 250)
})

// 切换「只看供应商」时立即用当前关键词重搜，
// 否则勾选状态变了而候选列表还是旧的，看起来像开关没生效。
watch(vendorOnly, () => {
  const keyword = query.value.trim()
  if (keyword) runSearch(keyword)
})

/**
 * 绑定成员。
 *
 * 传整个候选对象而非 userId：提升角色的提示里要带用户名，
 * 而响应中的 username 在旧数据上可能为空。
 */
async function add(user: { id: number; email: string; username: string }) {
  adding.value = true
  try {
    const member = await adminWorkspacesAPI.addMember(props.workspace.id, user.id)
    // role_promoted 由后端在「普通用户被顺带提升」时回报。
    // 全局角色被改动了，必须明说，不能悄悄发生。
    if (member.role_promoted) {
      appStore.showSuccess(
        t('admin.workspaces.memberAddPromoted', {
          name: user.username || user.email || `#${user.id}`
        })
      )
    } else {
      appStore.showSuccess(t('admin.workspaces.memberAdded'))
    }
    query.value = ''
    candidates.value = []
    searchOpen.value = false
    reload()
  } catch (err: any) {
    appStore.showError(err?.message || t('admin.workspaces.memberAddFailed'))
  } finally {
    adding.value = false
  }
}

async function remove(userId: number) {
  try {
    await adminWorkspacesAPI.removeMember(props.workspace.id, userId)
    appStore.showSuccess(t('admin.workspaces.memberRemoved'))
    reload()
  } catch (err: any) {
    appStore.showError(err?.message || t('common.deleteFailed'))
  }
}

onMounted(reload)

onBeforeUnmount(() => {
  if (searchTimer) clearTimeout(searchTimer)
  searchController?.abort()
})
</script>
