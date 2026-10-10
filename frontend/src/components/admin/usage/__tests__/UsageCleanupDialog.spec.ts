import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import UsageCleanupDialog from '../UsageCleanupDialog.vue'
import Pagination from '@/components/common/Pagination.vue'

const { createCleanupTask, listCleanupTasks, showError } = vi.hoisted(() => ({ createCleanupTask: vi.fn(), listCleanupTasks: vi.fn(), showError: vi.fn() }))
vi.mock('@/api/admin/usage', () => ({ adminUsageAPI: { createCleanupTask, listCleanupTasks } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess: vi.fn() }) }))
vi.mock('../UsageFilters.vue', () => ({ default: { template: '<div />' } }))
vi.mock('vue-i18n', async (importOriginal) => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))

enableAutoUnmount(afterEach)
beforeEach(() => { vi.resetAllMocks(); vi.spyOn(console, 'error').mockImplementation(() => {}) })
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks() })

async function mountDialog(startDate = '2026-09-08T20:59', endDate = '2026-09-08T21:00') {
  vi.useFakeTimers()
  listCleanupTasks.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 5 })
  createCleanupTask.mockResolvedValue({})
  const wrapper = mount(UsageCleanupDialog, {
    props: { show: false, startDate, endDate, filters: { model: 'requested-model', native_compaction_v2: true, billing_mode: 'image', upstream_model_mismatch: false, group_id: 5 } },
    global: { stubs: { BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' }, UsageFilters: true, DateRangePicker: true, ConfirmDialog: true, Pagination: true } },
  })
  await wrapper.setProps({ show: true })
  await flushPromises()
  return wrapper
}

describe('minute cleanup range contract', () => {
  it('keeps exact boundaries and every active filter in the confirmed payload', async () => {
    const wrapper = await mountDialog()
    const vm = wrapper.vm as any
    vm.openConfirm()
    vm.localStartDate = '2020-01-01T00:00'
    vm.localFilters.model = 'different-model'
    await vm.submitCleanup()
    const payload = createCleanupTask.mock.calls[0][0]
    expect(payload).toMatchObject({ start_time: new Date('2026-09-08T20:59').toISOString(), end_time: new Date('2026-09-08T21:00').toISOString(), model: 'requested-model', native_compaction_v2: true, billing_mode: 'image', upstream_model_mismatch: false, group_id: 5 })
    expect(payload).not.toHaveProperty('start_date')
    expect(payload).not.toHaveProperty('end_date')
    expect(new Date(payload.end_time).getTime() - new Date(payload.start_time).getTime()).toBe(60_000)
  })
  it('does not confirm or submit a reversed interval', async () => {
    const wrapper = await mountDialog('2026-09-08T21:00', '2026-09-08T20:59')
    const vm = wrapper.vm as any
    vm.openConfirm()
    await vm.submitCleanup()
    expect(vm.confirmVisible).toBe(false)
    expect(createCleanupTask).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalled()
  })
  it('preserves inclusive calendar-day legacy callers by converting the end once', async () => {
    const wrapper = await mountDialog('2026-09-08', '2026-09-08')
    const vm = wrapper.vm as any
    vm.openConfirm()
    await vm.submitCleanup()
    expect(createCleanupTask.mock.calls[0][0]).toMatchObject({ start_time: new Date('2026-09-08T00:00').toISOString(), end_time: new Date('2026-09-09T00:00').toISOString() })
  })
})

const page = (number: number) => ({ items: [{ id: number, status: 'succeeded', deleted_rows: number, filters: {} }], total: 15, page: number, page_size: 5 })
async function openDialog() {
  vi.useFakeTimers()
  listCleanupTasks.mockResolvedValueOnce(page(1))
  const wrapper = mount(UsageCleanupDialog, {
    props: { show: false, filters: {}, startDate: '2026-09-01', endDate: '2026-09-02' },
    global: { stubs: { BaseDialog: { template: '<div><slot/><slot name="footer"/></div>' }, UsageFilters: true, DateRangePicker: true, ConfirmDialog: true, Pagination: true } },
  })
  await wrapper.setProps({ show: true })
  await flushPromises()
  return wrapper
}

describe('cleanup task pagination', () => {
  it.each(['success', 'failure'])('ignores an older page request that completes with %s', async (outcome) => {
    const wrapper = await openDialog()
    let resolve!: (value: object) => void
    let reject!: (error: Error) => void
    listCleanupTasks.mockImplementationOnce(() => new Promise((res, rej) => { resolve = res; reject = rej }))
    wrapper.findComponent(Pagination).vm.$emit('update:page', 2)
    await flushPromises()
    listCleanupTasks.mockResolvedValueOnce(page(3))
    wrapper.findComponent(Pagination).vm.$emit('update:page', 3)
    await flushPromises()
    if (outcome === 'success') resolve(page(2))
    else reject(new Error('old failure'))
    await flushPromises()
    expect(wrapper.findComponent(Pagination).props('page')).toBe(3)
    expect(wrapper.text()).toContain('#3')
    expect(showError).not.toHaveBeenCalled()
  })

  it('ignores a pending result after the dialog is closed and reopened', async () => {
    const wrapper = await openDialog()
    let resolve!: (value: object) => void
    listCleanupTasks.mockImplementationOnce(() => new Promise(res => { resolve = res }))
    wrapper.findComponent(Pagination).vm.$emit('update:page', 2)
    await flushPromises()
    await wrapper.setProps({ show: false })
    listCleanupTasks.mockResolvedValueOnce(page(1))
    await wrapper.setProps({ show: true })
    await flushPromises()
    resolve(page(2))
    await flushPromises()
    expect(wrapper.findComponent(Pagination).props('page')).toBe(1)
    expect(wrapper.text()).toContain('#1')
  })
})
