<template>
  <main class="flex min-h-screen items-center justify-center bg-gray-50 p-5 text-gray-900 dark:bg-dark-900 dark:text-gray-100">
    <section class="w-full max-w-md space-y-5 rounded-2xl bg-white p-6 shadow-sm dark:bg-dark-800" aria-labelledby="verification-title">
      <h1 id="verification-title" class="text-xl font-semibold">手机端安全验证</h1>
      <p class="text-sm text-gray-600 dark:text-gray-300">完成站点验证后，将自动返回 Sub2 Mobile 登录。</p>
      <p v-if="loading" role="status">正在读取站点验证设置…</p>
      <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
      <p v-if="finished" role="status">{{ cancelled ? '已取消，请返回 App。' : '验证结果已提交，请返回 App。' }}</p>
      <CaptchaChallenge
        v-if="challenging && settings && provider"
        ref="captchaRef"
        :turnstile-enabled="provider === 'turnstile'"
        :turnstile-site-key="settings.turnstile_site_key || ''"
        :tencent-enabled="provider === 'tencent'"
        :tencent-app-id="settings.tencent_captcha_app_id || ''"
        :tencent-region="settings.tencent_captcha_region"
        :aliyun-enabled="provider === 'aliyun'"
        :aliyun-scene-id="settings.aliyun_captcha_scene_id"
        :aliyun-prefix="settings.aliyun_captcha_prefix"
        :aliyun-region="settings.aliyun_captcha_region"
        @verify="handleProof"
        @expire="failChallenge('验证码已过期，请重新验证。')"
        @error="failChallenge('验证码加载或验证失败，请检查网络后重试。')"
      />
      <div class="flex flex-col gap-3">
        <button
          v-if="!finished"
          type="button"
          class="btn btn-primary min-h-11 w-full"
          :disabled="loading || challenging || !bridge || !provider"
          @click="startChallenge"
        >{{ challenging ? '请完成安全验证…' : '开始验证 / 重试' }}</button>
        <button
          v-if="!finished && bridge"
          type="button"
          class="btn btn-secondary min-h-11 w-full"
          @click="cancel"
        >取消并返回</button>
        <button v-if="!loading && !settings && bridge" type="button" class="btn btn-secondary min-h-11" @click="loadSettings">
          重新读取设置
        </button>
      </div>
    </section>
  </main>
</template>

<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, shallowRef } from 'vue'
import CaptchaChallenge from '@/components/CaptchaChallenge.vue'
import {
  createMobileCaptchaBridge,
  readMobileBridgeEnvironment,
  resolveMobileCaptchaProvider,
  type MobileCaptchaProvider,
  type MobileCaptchaSettings
} from '@/utils/mobileCaptchaBridge'

const bridge = shallowRef<ReturnType<typeof createMobileCaptchaBridge> | null>(null)
const settings = shallowRef<MobileCaptchaSettings | null>(null)
const provider = ref<MobileCaptchaProvider | null>(null)
const captchaRef = ref<InstanceType<typeof CaptchaChallenge> | null>(null)
const loading = ref(true)
const challenging = ref(false)
const finished = ref(false)
const cancelled = ref(false)
const error = ref('')
let settingsRequest: AbortController | undefined
let challengeTimer: ReturnType<typeof setTimeout> | undefined
let generation = 0
let disposed = false

function stopChallenge() {
  generation++
  if (challengeTimer !== undefined) clearTimeout(challengeTimer)
  challengeTimer = undefined
  challenging.value = false
}

function failChallenge(message: string) {
  if (finished.value) return
  stopChallenge()
  error.value = message
}

function handleProof(token: string, randstr: string) {
  if (!challenging.value || finished.value || !provider.value) return
  try {
    if (bridge.value?.sendProof(provider.value, token, randstr)) {
      finished.value = true
      error.value = ''
      stopChallenge()
    }
  } catch {
    failChallenge('未能提交验证码，请返回 App 重新开始。')
  }
}

async function startChallenge() {
  if (!bridge.value || !provider.value || challenging.value || finished.value) return
  error.value = ''
  challenging.value = true
  const current = ++generation
  challengeTimer = setTimeout(() => failChallenge('验证等待超时，请重新验证。'), 120_000)
  await nextTick()
  if (current !== generation || !challenging.value) return
  if (provider.value !== 'turnstile') {
    const proof = await captchaRef.value?.verifyAction()
    if (current !== generation || !challenging.value) return
    if (proof) handleProof(proof.token, proof.randstr)
    else failChallenge(error.value || '验证未完成，可点击重试。')
  }
}

function cancel() {
  try {
    if (bridge.value?.cancel()) {
      stopChallenge()
      settingsRequest?.abort()
      cancelled.value = true
      finished.value = true
      error.value = ''
    }
  } catch {
    failChallenge('未能通知 App，请手动关闭验证窗口。')
  }
}

async function loadSettings() {
  settingsRequest?.abort()
  const request = new AbortController()
  settingsRequest = request
  const timeout = setTimeout(() => request.abort(), 30_000)
  loading.value = true
  error.value = ''
  try {
    // Do not use the authenticated Axios client, cookies, localStorage or a configurable API origin.
    const response = await fetch('/api/v1/settings/public', {
      credentials: 'omit', cache: 'no-store', signal: request.signal,
      headers: { Accept: 'application/json' }
    })
    if (!response.ok) throw new Error('settings')
    const envelope = await response.json()
    if (disposed || finished.value || settingsRequest !== request) return
    if (envelope.code !== 0 || !envelope.data || typeof envelope.data !== 'object') throw new Error('settings')
    const config = envelope.data as MobileCaptchaSettings
    provider.value = resolveMobileCaptchaProvider(config)
    settings.value = config
    if (!provider.value) error.value = '站点未启用验证码，请返回 App 重新登录。'
  } catch {
    if (!disposed && !finished.value && settingsRequest === request) {
      error.value = '无法读取站点验证码设置，请检查网络或联系管理员后重试。'
    }
  } finally {
    clearTimeout(timeout)
    if (settingsRequest === request) loading.value = false
  }
}

onMounted(async () => {
  try {
    bridge.value = createMobileCaptchaBridge(readMobileBridgeEnvironment)
  } catch (cause) {
    error.value = cause instanceof Error ? cause.message : '无法启动安全验证。'
    loading.value = false
    return
  }
  await loadSettings()
})

onBeforeUnmount(() => {
  disposed = true
  stopChallenge()
  settingsRequest?.abort()
})
</script>
