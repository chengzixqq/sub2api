import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import BulkEditUserModal from '../BulkEditUserModal.vue'

const { batchUpdateLimits, createAdjustmentIdempotencyKey, showSuccess, showError } = vi.hoisted(() => ({
  batchUpdateLimits: vi.fn(),
  createAdjustmentIdempotencyKey: vi.fn(() => 'test-adjustment-key'),
  showSuccess: vi.fn(),
  showError: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    users: {
      batchUpdateLimits,
      createAdjustmentIdempotencyKey
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showSuccess,
    showError
  })
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) =>
      params ? `${key}:${JSON.stringify(params)}` : key
  })
}))

const mountModal = () => mount(BulkEditUserModal, {
  props: {
    show: true,
    selectedIds: [4, 7]
  },
  global: {
    stubs: {
      BaseDialog: {
        props: ['show', 'title'],
        emits: ['close'],
        template: '<div v-if="show"><slot /><slot name="footer" /></div>'
      }
    }
  }
})

describe('BulkEditUserModal', () => {
  beforeEach(() => {
    batchUpdateLimits.mockReset()
    createAdjustmentIdempotencyKey.mockReset().mockReturnValue('test-adjustment-key')
    showSuccess.mockReset()
    showError.mockReset()
    batchUpdateLimits.mockResolvedValue({ affected: 2 })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('disables submission until at least one enabled field has a value', async () => {
    const wrapper = mountModal()

    expect(wrapper.get('[data-test="submit"]').attributes('disabled')).toBeDefined()

    await wrapper.get('[data-test="enable-concurrency"]').trigger('click')
    expect(wrapper.get('[data-test="submit"]').attributes('disabled')).toBeDefined()

    await wrapper.get('[data-test="concurrency-input"]').setValue('5')
    expect(wrapper.get('[data-test="submit"]').attributes('disabled')).toBeUndefined()
  })

  it('disables submission when more than 500 users are selected', async () => {
    const wrapper = mountModal()
    await wrapper.setProps({ selectedIds: Array.from({ length: 501 }, (_, index) => index + 1) })
    await wrapper.get('[data-test="enable-concurrency"]').trigger('click')
    await wrapper.get('[data-test="concurrency-input"]').setValue('5')

    expect(wrapper.text()).toContain('admin.users.bulkLimits.selectionLimit')
    expect(wrapper.get('[data-test="submit"]').attributes('disabled')).toBeDefined()
  })

  it('submits only the enabled RPM field and preserves zero as unlimited', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true)
    const wrapper = mountModal()

    await wrapper.get('[data-test="enable-rpm-limit"]').trigger('click')
    await wrapper.get('[data-test="rpm-limit-input"]').setValue('0')
    expect(wrapper.get('[data-test="adjustment-notes"]').attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('admin.users.bulkLimits.unlimited')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(batchUpdateLimits).toHaveBeenCalledWith({
      user_ids: [4, 7],
      all: false,
      rpm_limit: 0
    }, { idempotencyKey: 'test-adjustment-key' })
    expect(confirm).toHaveBeenCalledWith(
      expect.stringContaining('admin.users.bulkLimits.rpmUnlimitedValue')
    )
    expect(wrapper.emitted('success')).toEqual([[2]])
  })

  it('omits disabled fields from the request', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const wrapper = mountModal()

    await wrapper.get('[data-test="enable-concurrency"]').trigger('click')
    await wrapper.get('[data-test="concurrency-input"]').setValue('9')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(batchUpdateLimits).toHaveBeenCalledWith({
      user_ids: [4, 7],
      all: false,
      concurrency: 9
    }, { idempotencyKey: 'test-adjustment-key' })
  })

  it('trims and sends the optional operation notes', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const wrapper = mountModal()

    await wrapper.get('[data-test="enable-concurrency"]').trigger('click')
    await wrapper.get('[data-test="concurrency-input"]').setValue('9')
    await wrapper.get('[data-test="adjustment-notes"]').setValue('  planned capacity change  ')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(batchUpdateLimits).toHaveBeenCalledWith({
      user_ids: [4, 7],
      all: false,
      concurrency: 9,
      notes: 'planned capacity change'
    }, { idempotencyKey: 'test-adjustment-key' })
  })

  it('does not call the API when overwrite confirmation is cancelled', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(false)
    const wrapper = mountModal()

    await wrapper.get('[data-test="enable-concurrency"]').trigger('click')
    await wrapper.get('[data-test="concurrency-input"]').setValue('9')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(batchUpdateLimits).not.toHaveBeenCalled()
  })

  it('reuses the pending key after failure and rotates it when limits change', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const error = new Error('network result unknown')
    batchUpdateLimits
      .mockReset()
      .mockRejectedValueOnce(error)
      .mockRejectedValueOnce(error)
      .mockResolvedValueOnce({ affected: 2 })
    createAdjustmentIdempotencyKey
      .mockReset()
      .mockReturnValueOnce('key-1')
      .mockReturnValueOnce('key-2')
    const wrapper = mountModal()
    await wrapper.get('[data-test="enable-concurrency"]').trigger('click')
    const concurrencyInput = wrapper.get('[data-test="concurrency-input"]')

    await concurrencyInput.setValue('5')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(batchUpdateLimits.mock.calls[0][1]).toEqual({ idempotencyKey: 'key-1' })
    expect(batchUpdateLimits.mock.calls[1][1]).toEqual({ idempotencyKey: 'key-1' })
    expect(createAdjustmentIdempotencyKey).toHaveBeenCalledOnce()

    await concurrencyInput.setValue('6')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(batchUpdateLimits.mock.calls[2][1]).toEqual({ idempotencyKey: 'key-2' })
    expect(createAdjustmentIdempotencyKey).toHaveBeenCalledTimes(2)
  })
})
