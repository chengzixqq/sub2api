import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import type { AdminUser } from '@/types'

const { update, createAdjustmentIdempotencyKey, updateUserAttributeValues, showError, showSuccess, run } = vi.hoisted(() => ({
  update: vi.fn(),
  createAdjustmentIdempotencyKey: vi.fn(() => 'test-adjustment-key'),
  updateUserAttributeValues: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
  run: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    users: { update, createAdjustmentIdempotencyKey },
    userAttributes: { updateUserAttributeValues }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess })
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copyToClipboard: vi.fn() })
}))

vi.mock('@/composables/useStepUp', () => ({
  useStepUp: () => ({ run }),
  isStepUpBlocked: () => false,
  isStepUpCancelled: () => false,
  stepUpBlockReason: () => null
}))

vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key })
}))

import UserEditModal from '../UserEditModal.vue'

const user = {
  id: 7,
  email: 'user@example.com',
  username: 'User',
  notes: 'persistent account note',
  role: 'user',
  concurrency: 2,
  rpm_limit: 0
} as AdminUser

function mountModal(show = true) {
  return mount(UserEditModal, {
    props: { show, user },
    global: {
      stubs: {
        BaseDialog: {
          props: ['show', 'title'],
          template: '<div v-if="show"><slot /><slot name="footer" /></div>'
        },
        UserAttributeForm: true,
        TotpStepUpDialog: true,
        Icon: true
      }
    }
  })
}

describe('UserEditModal concurrency adjustment notes', () => {
  beforeEach(() => {
    update.mockReset().mockResolvedValue(user)
    createAdjustmentIdempotencyKey.mockReset().mockReturnValue('test-adjustment-key')
    updateUserAttributeValues.mockReset()
    showError.mockReset()
    showSuccess.mockReset()
    run.mockReset().mockImplementation((operation: () => Promise<unknown>) => operation())
  })

  it('keeps the temporary ledger note separate from persistent user notes', async () => {
    const wrapper = mountModal()
    const concurrencyInput = wrapper.findAll('input[type="number"]')[0]
    const adjustmentNotes = wrapper.get('[data-test="adjustment-notes"]')

    expect(adjustmentNotes.attributes('disabled')).toBeDefined()
    await concurrencyInput.setValue('5')
    expect(adjustmentNotes.attributes('disabled')).toBeUndefined()
    await adjustmentNotes.setValue('  temporary capacity correction  ')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(update).toHaveBeenCalledWith(7, expect.objectContaining({
      notes: 'persistent account note',
      concurrency: 5,
      adjustment_notes: 'temporary capacity correction'
    }), { idempotencyKey: 'test-adjustment-key' })
  })

  it('clears the temporary note whenever the modal is reopened', async () => {
    const wrapper = mountModal()
    const concurrencyInput = wrapper.findAll('input[type="number"]')[0]
    await concurrencyInput.setValue('5')
    await wrapper.get('[data-test="adjustment-notes"]').setValue('one-time note')

    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })

    expect((wrapper.get('[data-test="adjustment-notes"]').element as HTMLTextAreaElement).value).toBe('')
  })

  it('does not send adjustment_notes when concurrency is unchanged', async () => {
    const wrapper = mountModal()

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(update).toHaveBeenCalledOnce()
    expect(update.mock.calls[0][1]).not.toHaveProperty('adjustment_notes')
  })

  it('reuses the pending key after failure and rotates it when concurrency changes', async () => {
    const error = new Error('network result unknown')
    update
      .mockReset()
      .mockRejectedValueOnce(error)
      .mockRejectedValueOnce(error)
      .mockResolvedValueOnce(user)
    createAdjustmentIdempotencyKey
      .mockReset()
      .mockReturnValueOnce('key-1')
      .mockReturnValueOnce('key-2')
    const wrapper = mountModal()
    const concurrencyInput = wrapper.findAll('input[type="number"]')[0]

    await concurrencyInput.setValue('5')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(update.mock.calls[0][2]).toEqual({ idempotencyKey: 'key-1' })
    expect(update.mock.calls[1][2]).toEqual({ idempotencyKey: 'key-1' })
    expect(createAdjustmentIdempotencyKey).toHaveBeenCalledOnce()

    await concurrencyInput.setValue('6')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(update.mock.calls[2][2]).toEqual({ idempotencyKey: 'key-2' })
    expect(createAdjustmentIdempotencyKey).toHaveBeenCalledTimes(2)
  })
})
