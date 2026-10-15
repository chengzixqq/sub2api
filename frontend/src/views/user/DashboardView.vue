<template>
  <AppLayout>
    <div class="space-y-6">
      <div v-if="loading && !stats" class="flex items-center justify-center py-12"><LoadingSpinner /></div>
      <UserDashboardStats v-else-if="stats" :stats="stats" :balance="user?.balance || 0" :is-simple="authStore.isSimpleMode" :platform-quotas="platformQuotas" />
      <button v-else class="btn btn-secondary" @click="loadStats">{{ t('common.retry') }}</button>
      <UserDashboardCharts v-model:startDate="startDate" v-model:endDate="endDate" v-model:granularity="granularity" :loading="loadingCharts" :trend="trendData" :models="modelStats" @dateRangeChange="loadCharts" @granularityChange="loadCharts" @refresh="refreshAll" />
      <div class="grid grid-cols-1 gap-6 lg:grid-cols-3">
        <div class="lg:col-span-2"><UserDashboardRecentUsage :data="recentUsage" :loading="loadingUsage" /></div>
        <div class="lg:col-span-1"><UserDashboardQuickActions /></div>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { usageAPI, type UserDashboardStats as UserStatsType } from '@/api/usage'
import AppLayout from '@/components/layout/AppLayout.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import UserDashboardStats from '@/components/user/dashboard/UserDashboardStats.vue'
import UserDashboardCharts from '@/components/user/dashboard/UserDashboardCharts.vue'
import UserDashboardRecentUsage from '@/components/user/dashboard/UserDashboardRecentUsage.vue'
import UserDashboardQuickActions from '@/components/user/dashboard/UserDashboardQuickActions.vue'
import type { UsageLog, TrendDataPoint, ModelStat, PlatformQuotaItem } from '@/types'
import { getMyPlatformQuotas } from '@/api/user'
import { formatDateLocalInput } from '@/utils/format'

const { t } = useI18n()
const authStore = useAuthStore()
const user = computed(() => authStore.user)
const stats = ref<UserStatsType | null>(null)
const loading = ref(false)
const loadingUsage = ref(false)
const loadingCharts = ref(false)
const trendData = ref<TrendDataPoint[]>([])
const modelStats = ref<ModelStat[]>([])
const recentUsage = ref<UsageLog[]>([])
const platformQuotas = ref<PlatformQuotaItem[] | null>(null)

const startDate = ref(formatDateLocalInput(new Date(Date.now() - 6 * 86400000)))
const endDate = ref(formatDateLocalInput(new Date()))
const granularity = ref<'day' | 'hour'>('day')
let statsController: AbortController | null = null
let chartsController: AbortController | null = null
let recentController: AbortController | null = null
let quotasController: AbortController | null = null

const loadStats = async () => {
  statsController?.abort()
  const controller = new AbortController()
  statsController = controller
  loading.value = true
  try {
    const response = await usageAPI.getDashboardStats({ signal: controller.signal })
    if (!controller.signal.aborted) stats.value = response
  } catch (error) {
    if (!controller.signal.aborted) console.error('Failed to load dashboard stats:', error)
  } finally {
    if (statsController === controller) loading.value = false
  }
}

const loadCharts = async () => {
  chartsController?.abort()
  const controller = new AbortController()
  chartsController = controller
  loadingCharts.value = true
  const params = { start_date: startDate.value, end_date: endDate.value }
  const options = { signal: controller.signal }
  try {
    const [trend, models] = await Promise.all([
      usageAPI.getDashboardTrend({ ...params, granularity: granularity.value }, options),
      usageAPI.getDashboardModels(params, options)
    ])
    if (controller.signal.aborted) return
    trendData.value = trend.trend || []
    modelStats.value = models.models || []
  } catch (error) {
    if (!controller.signal.aborted) console.error('Failed to load charts:', error)
  } finally {
    if (chartsController === controller) loadingCharts.value = false
  }
}

const loadRecent = async () => {
  recentController?.abort()
  const controller = new AbortController()
  recentController = controller
  loadingUsage.value = true
  try {
    const response = await usageAPI.getByDateRange(startDate.value, endDate.value, undefined, {
      pageSize: 5,
      countMode: 'deferred',
      signal: controller.signal
    })
    if (!controller.signal.aborted) recentUsage.value = response.items
  } catch (error) {
    if (!controller.signal.aborted) console.error('Failed to load recent usage:', error)
  } finally {
    if (recentController === controller) loadingUsage.value = false
  }
}

const loadPlatformQuotas = async () => {
  quotasController?.abort()
  const controller = new AbortController()
  quotasController = controller
  try {
    const data = await getMyPlatformQuotas({ signal: controller.signal })
    if (!controller.signal.aborted) platformQuotas.value = data.platform_quotas ?? []
  } catch (error) {
    if (controller.signal.aborted) return
    console.warn('Failed to load platform quotas:', error)
    platformQuotas.value = []
  }
}

const refreshAll = () => {
  // Refresh balance independently; usage statistics do not depend on /auth/me.
  void authStore.refreshUser().catch((error) => console.error('Failed to refresh user:', error))
  void loadStats()
  void loadCharts()
  void loadRecent()
  void loadPlatformQuotas()
}

onMounted(refreshAll)
onUnmounted(() => {
  statsController?.abort()
  chartsController?.abort()
  recentController?.abort()
  quotasController?.abort()
})
</script>
