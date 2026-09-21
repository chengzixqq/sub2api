import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import Pagination from '../Pagination.vue'
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('deferred count pagination', () => {
  const mountPagination = () => mount(Pagination, {
    props: { page: 2, pageSize: 20, total: null, hasMore: true, itemCount: 20, showJump: true },
    global: {
      stubs: { Select: true, Icon: true },
    },
  })
  it('does not invent a total or expose a last-page jump', async () => {
    const wrapper = mountPagination()
    expect(wrapper.text()).toContain('pagination.totalPending')
    expect(wrapper.find('input[type="number"]').exists()).toBe(false)
    await wrapper.find('button[aria-label="pagination.next"]').trigger('click')
    expect(wrapper.emitted('update:page')).toEqual([[3]])
    await wrapper.setProps({ hasMore: false })
    expect(wrapper.find('button[aria-label="pagination.next"]').attributes('disabled')).toBeDefined()
  })
  it('enables exact pages only after matching statistics arrive', async () => {
    const wrapper = mountPagination()
    await wrapper.setProps({ total: 105 })
    expect(wrapper.text()).not.toContain('pagination.totalPending')
    expect(wrapper.find('input[type="number"]').attributes('max')).toBe('6')
  })
  it('allows next page when has_more contradicts an older cached total', async () => {
    const wrapper = mountPagination()
    await wrapper.setProps({ page: 1, total: 20, hasMore: true })
    const next = wrapper.find('button[aria-label="pagination.next"]')
    expect(next.attributes('disabled')).toBeUndefined()
    await next.trigger('click')
    expect(wrapper.emitted('update:page')).toEqual([[2]])
  })
})
