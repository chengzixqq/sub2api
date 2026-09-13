import { mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import { baseCompile } from '@intlify/message-compiler'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import DataTable from '../DataTable.vue'
import { enTableColumns, zhTableColumns } from '@/i18n/locales/tableColumns'

function compileMessages(messages: Record<string, string>) {
  return Object.fromEntries(Object.entries(messages).map(([key, value]) => [
    key, new Function(`return ${baseCompile(value).code}`)()
  ]))
}

function viewport(desktop: boolean) {
  window.matchMedia = vi.fn().mockImplementation((media: string) => ({
    matches: desktop, media, addEventListener: vi.fn(), removeEventListener: vi.fn()
  }))
}

function mountTable(desktop = true) {
  viewport(desktop)
  const pinia = createPinia()
  pinia.state.value.auth = { user: { id: 66, role: 'admin' }, workspace: null }
  const i18n = createI18n({
    legacy: false, locale: 'en',
    messages: { en: { tableColumns: compileMessages(enTableColumns) }, zh: { tableColumns: compileMessages(zhTableColumns) } }
  })
  const wrapper = mount(DataTable, {
    props: {
      columnOrderKey: 'test.orders',
      columns: [
        { key: 'select', label: '' },
        { key: 'id', label: 'Order', sortable: true },
        { key: 'user', label: 'User', sortable: true },
        { key: 'amount', label: 'Amount' },
        { key: 'actions', label: 'Actions' }
      ],
      data: [{ id: 1, user: 'A', amount: 12 }],
      serverSideSort: true
    },
    global: { plugins: [pinia, i18n], stubs: { Teleport: true } }
  })
  return { wrapper, pinia, i18n }
}

describe('DataTable column order', () => {
  beforeEach(() => localStorage.clear())

  it('inserts a dragged header without sorting the rows or moving pinned columns', async () => {
    const { wrapper } = mountTable()
    const headers = () => wrapper.findAll('th[data-column-key]').map(header => header.attributes('data-column-key'))
    const user = wrapper.get('th[data-column-key="user"]')
    const order = wrapper.get('th[data-column-key="id"]')
    expect(wrapper.get('th[data-column-key="select"]').attributes('draggable')).toBe('false')
    expect(wrapper.get('th[data-column-key="actions"]').attributes('draggable')).toBe('false')
    await user.trigger('dragstart', { dataTransfer: { setData: vi.fn() } })
    await order.trigger('dragover', { clientX: 0 })
    await order.trigger('drop', { clientX: 0 })
    await order.trigger('click')
    expect(headers()).toEqual(['select', 'user', 'id', 'amount', 'actions'])
    expect(wrapper.emitted('sort')).toBeUndefined()
    expect(wrapper.findAll('tbody td').map(cell => cell.text())).toEqual(['', 'A', '1', '12', ''])
    expect(localStorage.length).toBe(1)
    wrapper.unmount()
  })

  it('supports mobile move controls, localized labels and default reset', async () => {
    const { wrapper, i18n } = mountTable(false)
    expect(wrapper.get('[data-test="column-order-toggle"]').attributes('aria-label')).toBe('Column order')
    await wrapper.get('[data-test="column-order-toggle"]').trigger('click')
    await wrapper.get('[data-move-earlier="user"]').trigger('click')
    expect(wrapper.findAll('[data-field]').map(field => field.attributes('data-field'))).toEqual(['select', 'user', 'id', 'amount'])
    expect(wrapper.get('[data-move-earlier="user"]').attributes('disabled')).toBeDefined()
    i18n.global.locale.value = 'zh'
    await wrapper.vm.$nextTick()
    expect(wrapper.get('[data-test="column-order-toggle"]').attributes('aria-label')).toBe('列顺序')
    await wrapper.get('[data-test="column-order-reset"]').trigger('click')
    expect(wrapper.findAll('[data-field]').map(field => field.attributes('data-field'))).toEqual(['select', 'id', 'user', 'amount'])
    expect(localStorage.length).toBe(0)
    wrapper.unmount()
  })

  it('changes persisted preference when the active account or workspace changes', async () => {
    const { wrapper, pinia } = mountTable()
    const headers = () => wrapper.findAll('th[data-column-key]').map(header => header.attributes('data-column-key'))
    await wrapper.get('[data-test="column-order-toggle"]').trigger('click')
    await wrapper.get('[data-move-earlier="user"]').trigger('click')
    pinia.state.value.auth.user.id = 67
    await wrapper.vm.$nextTick()
    expect(headers()).toEqual(['select', 'id', 'user', 'amount', 'actions'])
    pinia.state.value.auth.user.id = 66
    await wrapper.vm.$nextTick()
    expect(headers()).toEqual(['select', 'user', 'id', 'amount', 'actions'])
    pinia.state.value.auth.workspace = { id: 9 }
    await wrapper.vm.$nextTick()
    expect(headers()).toEqual(['select', 'id', 'user', 'amount', 'actions'])
    wrapper.unmount()
  })

  it('does not expose ordering controls on an excluded table', async () => {
    const { wrapper } = mountTable()
    await wrapper.setProps({ columnOrderKey: undefined })
    expect(wrapper.find('[data-test="column-order-toggle"]').exists()).toBe(false)
    expect(wrapper.findAll('th[data-column-key]').every(header => header.attributes('draggable') === 'false')).toBe(true)
    await wrapper.get('th[data-column-key="id"]').trigger('click')
    expect(wrapper.emitted('sort')).toEqual([['id', 'asc']])
    wrapper.unmount()
  })
})
