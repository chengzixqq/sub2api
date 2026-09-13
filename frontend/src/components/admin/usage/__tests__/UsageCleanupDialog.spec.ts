import { afterEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import UsageCleanupDialog from '../UsageCleanupDialog.vue'

const { createCleanupTask, listCleanupTasks, showError } = vi.hoisted(() => ({ createCleanupTask: vi.fn(), listCleanupTasks: vi.fn(), showError: vi.fn() }))
vi.mock('@/api/admin/usage', () => ({ adminUsageAPI: { createCleanupTask, listCleanupTasks } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess: vi.fn() }) }))
vi.mock('../UsageFilters.vue', () => ({ default: { template: '<div />' } }))
vi.mock('vue-i18n', async (importOriginal) => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))

afterEach(() => { vi.useRealTimers(); vi.clearAllMocks() })

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
    wrapper.unmount()
  })
  it('does not confirm or submit a reversed interval', async () => {
    const wrapper = await mountDialog('2026-09-08T21:00', '2026-09-08T20:59')
    const vm = wrapper.vm as any
    vm.openConfirm()
    await vm.submitCleanup()
    expect(vm.confirmVisible).toBe(false)
    expect(createCleanupTask).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalled()
    wrapper.unmount()
  })
  it('preserves inclusive calendar-day legacy callers by converting the end once', async () => {
    const wrapper = await mountDialog('2026-09-08', '2026-09-08')
    const vm = wrapper.vm as any
    vm.openConfirm()
    await vm.submitCleanup()
    expect(createCleanupTask.mock.calls[0][0]).toMatchObject({ start_time: new Date('2026-09-08T00:00').toISOString(), end_time: new Date('2026-09-09T00:00').toISOString() })
    wrapper.unmount()
  })
})
