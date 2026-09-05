<template>
  <BaseDialog :show="show" :title="operation === 'add' ? t('admin.users.deposit') : t('admin.users.withdraw')" width="narrow" @close="handleClose">
    <form v-if="user" id="balance-form" @submit.prevent="handleBalanceSubmit" class="space-y-5">
      <div class="flex items-center gap-3 rounded-xl bg-gray-50 p-4 dark:bg-dark-700">
        <div class="flex h-10 w-10 items-center justify-center rounded-full bg-primary-100"><span class="text-lg font-medium text-primary-700">{{ user.email.charAt(0).toUpperCase() }}</span></div>
        <div class="flex-1"><p class="font-medium text-gray-900 dark:text-gray-100">{{ user.email }}</p><p class="text-sm text-gray-500 dark:text-gray-400">{{ t('admin.users.currentBalance') }}: ${{ formatBalance(user.balance) }}</p></div>
      </div>
      <div>
        <label class="input-label">{{ operation === 'add' ? t('admin.users.depositAmount') : t('admin.users.withdrawAmount') }}</label>
        <div class="relative flex gap-2">
          <div class="relative flex-1"><div class="absolute left-3 top-1/2 -translate-y-1/2 font-medium text-gray-500">$</div><input v-model="form.amount" type="text" inputmode="decimal" required class="input pl-8" /></div>
          <button v-if="operation === 'subtract'" type="button" @click="fillAllBalance" class="btn btn-secondary whitespace-nowrap">{{ t('admin.users.withdrawAll') }}</button>
        </div>
      </div>
      <div><label class="input-label">{{ t('admin.users.notes') }}</label><textarea v-model="form.notes" rows="3" class="input"></textarea></div>
      <div v-if="isValidAmount" class="rounded-xl border border-blue-200 bg-blue-50 p-4 dark:border-blue-800 dark:bg-blue-950"><div class="flex items-center justify-between text-sm"><span class="text-gray-700 dark:text-gray-300">{{ t('admin.users.newBalance') }}:</span><span class="font-bold text-gray-900 dark:text-gray-100">${{ calculateNewBalance() }}</span></div></div>
    </form>
    <template #footer>
      <div class="flex justify-end gap-3">
        <button @click="handleClose" class="btn btn-secondary">{{ t('common.cancel') }}</button>
        <button type="submit" form="balance-form" :disabled="submitting || !form.amount" class="btn" :class="operation === 'add' ? 'bg-emerald-600 text-white' : 'btn-danger'">{{ submitting ? t('common.saving') : t('common.confirm') }}</button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type { AdminUser } from '@/types'
import BaseDialog from '@/components/common/BaseDialog.vue'

const props = defineProps<{ show: boolean, user: AdminUser | null, operation: 'add' | 'subtract' }>()
const emit = defineEmits(['close', 'success']); const { t } = useI18n(); const appStore = useAppStore()

const submitting = ref(false); const form = reactive({ amount: '', notes: '' })
const pendingAdjustment = ref<{ fingerprint: string, idempotencyKey: string } | null>(null)

const clearPendingAdjustment = () => { pendingAdjustment.value = null }
const getAdjustmentOptions = (fingerprint: string) => {
  if (pendingAdjustment.value?.fingerprint !== fingerprint) {
    pendingAdjustment.value = {
      fingerprint,
      idempotencyKey: adminAPI.users.createAdjustmentIdempotencyKey()
    }
  }
  return { idempotencyKey: pendingAdjustment.value.idempotencyKey }
}
const handleClose = () => {
  clearPendingAdjustment()
  emit('close')
}

watch(() => props.show, (v) => {
  if (v) {
    form.amount = ''
    form.notes = ''
    clearPendingAdjustment()
  } else {
    clearPendingAdjustment()
  }
})

const decimalPattern = /^\d{1,12}(?:\.\d{1,8})?$/
const amountUnits = computed(() => decimalToUnits(form.amount))
const isValidAmount = computed(() => amountUnits.value !== null && amountUnits.value > 0n)

function decimalToUnits(value: string): bigint | null {
  const normalized = value.trim()
  if (!decimalPattern.test(normalized)) return null
  const [integer, fraction = ''] = normalized.split('.')
  return BigInt(integer) * 100000000n + BigInt(fraction.padEnd(8, '0'))
}

function unitsToDecimal(value: bigint): string {
  const negative = value < 0n
  const absolute = negative ? -value : value
  const integer = absolute / 100000000n
  const fraction = (absolute % 100000000n).toString().padStart(8, '0').replace(/0+$/, '')
  const formatted = fraction ? `${integer}.${fraction}` : `${integer}.00`
  return negative ? `-${formatted}` : formatted
}

// 格式化余额：显示完整精度，去除尾部多余的0
const formatBalance = (value: number) => {
  if (value === 0) return '0.00'
  // 最多保留8位小数，去除尾部的0
  const formatted = value.toFixed(8).replace(/\.?0+$/, '')
  // 确保至少有2位小数
  const parts = formatted.split('.')
  if (parts.length === 1) return formatted + '.00'
  if (parts[1].length === 1) return formatted + '0'
  return formatted
}

// 填入全部余额
const fillAllBalance = () => {
  if (props.user) {
    form.amount = props.user.balance.toFixed(8).replace(/\.?0+$/, '')
  }
}

const calculateNewBalance = () => {
	if (!props.user || amountUnits.value === null) return '0.00'
	const current = decimalToUnits(props.user.balance.toFixed(8)) ?? 0n
	const result = props.operation === 'add' ? current + amountUnits.value : current - amountUnits.value
	return unitsToDecimal(result)
}
const handleBalanceSubmit = async () => {
  if (!props.user) return
	if (!isValidAmount.value) {
    appStore.showError(t('admin.users.amountRequired'))
    return
  }
	const currentUnits = decimalToUnits(props.user.balance.toFixed(8)) ?? 0n
	if (props.operation === 'subtract' && amountUnits.value! > currentUnits) {
    appStore.showError(t('admin.users.insufficientBalance'))
    return
  }
  submitting.value = true
  try {
    const fingerprint = JSON.stringify([props.user.id, props.operation, form.amount, form.notes])
    const options = getAdjustmentOptions(fingerprint)
    await adminAPI.users.updateBalance(props.user.id, form.amount, props.operation, form.notes, options)
    clearPendingAdjustment()
    appStore.showSuccess(t('common.success')); emit('success'); emit('close')
  } catch (e: any) {
    console.error('Failed to update balance:', e)
    appStore.showError(e.response?.data?.detail || t('common.error'))
  } finally { submitting.value = false }
}
</script>
