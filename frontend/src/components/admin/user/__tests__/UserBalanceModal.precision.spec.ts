import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import type { AdminUser } from '@/types'

const { updateBalance, createAdjustmentIdempotencyKey, showError, showSuccess } = vi.hoisted(() => ({
  updateBalance: vi.fn(),
  createAdjustmentIdempotencyKey: vi.fn(() => 'test-adjustment-key'),
  showError: vi.fn(),
  showSuccess: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: { users: { updateBalance, createAdjustmentIdempotencyKey } }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess })
}))

vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key })
}))

import UserBalanceModal from '../UserBalanceModal.vue'

describe('UserBalanceModal decimal precision', () => {
  beforeEach(() => {
    updateBalance.mockReset().mockResolvedValue({})
    createAdjustmentIdempotencyKey.mockReset().mockReturnValue('test-adjustment-key')
    showError.mockReset()
    showSuccess.mockReset()
  })

  it('submits the exact decimal token without converting it to a number', async () => {
    const user = { id: 9, email: 'user@example.com', balance: 0 } as AdminUser
    const wrapper = mount(UserBalanceModal, {
      props: { show: true, user, operation: 'add' },
      global: {
        stubs: {
          BaseDialog: {
            props: ['show', 'title'],
            template: '<div v-if="show"><slot /><slot name="footer" /></div>'
          }
        }
      }
    })

    await wrapper.find('input[inputmode="decimal"]').setValue('999999999999.00000001')
    await wrapper.find('textarea').setValue('precision test')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(updateBalance).toHaveBeenCalledWith(
      9,
      '999999999999.00000001',
      'add',
      'precision test',
      { idempotencyKey: 'test-adjustment-key' }
    )
    expect(showError).not.toHaveBeenCalled()
  })

  it('reuses the pending key after failure and rotates it when the adjustment changes', async () => {
    const error = new Error('network result unknown')
    updateBalance
      .mockReset()
      .mockRejectedValueOnce(error)
      .mockRejectedValueOnce(error)
      .mockResolvedValueOnce({})
    createAdjustmentIdempotencyKey
      .mockReset()
      .mockReturnValueOnce('key-1')
      .mockReturnValueOnce('key-2')

    const wrapper = mount(UserBalanceModal, {
      props: {
        show: true,
        user: { id: 9, email: 'user@example.com', balance: 0 } as AdminUser,
        operation: 'add'
      },
      global: {
        stubs: {
          BaseDialog: {
            props: ['show', 'title'],
            template: '<div v-if="show"><slot /><slot name="footer" /></div>'
          }
        }
      }
    })

    const amount = wrapper.find('input[inputmode="decimal"]')
    await amount.setValue('5.25')
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(updateBalance.mock.calls[0][4]).toEqual({ idempotencyKey: 'key-1' })
    expect(updateBalance.mock.calls[1][4]).toEqual({ idempotencyKey: 'key-1' })
    expect(createAdjustmentIdempotencyKey).toHaveBeenCalledOnce()

    await amount.setValue('6.25')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(updateBalance.mock.calls[2][4]).toEqual({ idempotencyKey: 'key-2' })
    expect(createAdjustmentIdempotencyKey).toHaveBeenCalledTimes(2)
  })
})
