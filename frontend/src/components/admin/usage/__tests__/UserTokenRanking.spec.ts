import { describe, expect, it, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

import UserTokenRanking from '../UserTokenRanking.vue'

const getUserBreakdown = vi.fn()

vi.mock('@/api/admin/dashboard', () => ({
  getUserBreakdown: (...args: unknown[]) => getUserBreakdown(...args),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

const item = (id: number, tokens: number) => ({
  user_id: id,
  email: `u${id}@test.com`,
  requests: 1,
  input_tokens: tokens,
  output_tokens: 0,
  cache_tokens: 0,
  total_tokens: tokens,
  actual_cost: 0.5,
})

const mountRanking = (props: Record<string, unknown> = {}) =>
  mount(UserTokenRanking, {
    props: {
      startDate: '2026-07-01',
      endDate: '2026-07-08',
      filters: {},
      ...props,
    },
    global: { stubs: { Select: true, LoadingSpinner: true } },
  })

describe('UserTokenRanking', () => {
  beforeEach(() => {
    window.matchMedia = vi.fn().mockImplementation((media: string) => ({
      matches: true, media, addEventListener: vi.fn(), removeEventListener: vi.fn(),
    }))
    getUserBreakdown.mockReset()
    getUserBreakdown.mockResolvedValue({ users: [item(1, 100), item(2, 50)] })
  })

  it('uses shared column ordering without changing the requested ranking metric', async () => {
    const wrapper = mountRanking()
    await flushPromises()
    expect(wrapper.find('[data-test="column-order-toggle"]').exists()).toBe(true)
    expect(getUserBreakdown.mock.calls.at(-1)![0].sort_by).toBe('total_tokens')
    wrapper.unmount()
  })

  it('loads on mount with shared filters and emits select-user with id + email on row click', async () => {
    const wrapper = mountRanking({ filters: { group_id: 3 }, model: 'claude-fable-5' })
    await flushPromises()

    expect(getUserBreakdown).toHaveBeenCalledWith(expect.objectContaining({
      group_id: 3,
      model: 'claude-fable-5',
      start_date: '2026-07-01',
      end_date: '2026-07-08',
      sort_by: 'total_tokens',
      limit: 50,
    }), expect.objectContaining({ signal: expect.any(AbortSignal) }))

    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(2)

    await rows[0].trigger('click')
    expect(wrapper.emitted('select-user')![0]).toEqual([1, 'u1@test.com'])
  })

  it('reloads when shared filters change', async () => {
    const wrapper = mountRanking()
    await flushPromises()
    expect(getUserBreakdown).toHaveBeenCalledTimes(1)

    await wrapper.setProps({ filters: { user_id: 9 } })
    await flushPromises()

    expect(getUserBreakdown).toHaveBeenCalledTimes(2)
    expect(getUserBreakdown).toHaveBeenLastCalledWith(expect.objectContaining({ user_id: 9 }), expect.anything())
  })

  it('uses exact shared boundaries and ignores a late previous ranking', async () => {
    let resolveOld!: (value: any) => void
    getUserBreakdown.mockImplementationOnce(() => new Promise((resolve) => { resolveOld = resolve }))
    const first = { start_time: '2026-09-01T04:00:00Z', end_time: '2026-09-08T04:00:00Z', billing_mode: 'image' }
    const wrapper = mountRanking({ filters: first })
    const signal = getUserBreakdown.mock.calls[0][1].signal as AbortSignal
    await wrapper.setProps({ filters: { ...first, end_time: '2026-09-02T04:00:00Z' } })
    await flushPromises()
    resolveOld({ users: [item(99, 999)] })
    await flushPromises()
    expect(signal.aborted).toBe(true)
    expect(wrapper.text()).not.toContain('u99@test.com')
    expect(getUserBreakdown.mock.calls.at(-1)![0]).toMatchObject({ billing_mode: 'image', start_date: undefined, end_date: undefined, end_time: '2026-09-02T04:00:00Z' })
    wrapper.unmount()
    expect(getUserBreakdown.mock.calls.at(-1)![1].signal.aborted).toBe(true)
  })
})
