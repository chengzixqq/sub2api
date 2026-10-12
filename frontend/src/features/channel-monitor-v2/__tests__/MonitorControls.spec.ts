import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import Select from '@/components/common/Select.vue'
import MonitorHealthMatrix from '../MonitorHealthMatrix.vue'
import MonitorTrendPanel from '../MonitorTrendPanel.vue'
import MonitorViewSwitcher from '../MonitorViewSwitcher.vue'
import { getMonitorPreviewScenario } from '../monitorPreview'

vi.mock('vue-i18n', async importOriginal => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key, te: () => false, locale: { value: 'en' } }) }))
vi.mock('vue-chartjs', () => ({ Line: { props: ['data', 'options'], template: '<div />' } }))

afterEach(() => { document.body.innerHTML = '' })

describe('monitor native controls', () => {
  it('opens time buckets by default and switches to model details with the native selector', async () => {
    const overview = getMonitorPreviewScenario('normal').overview!
    const wrapper = mount(MonitorHealthMatrix, { props: { items: overview.items, coverage: overview.coverage }, attachTo: document.body })
    await wrapper.findAll('tbody tr')[0].get('td button').trigger('click')
    expect(wrapper.emitted('bucket')?.[0]?.[1]).toEqual(expect.objectContaining({ bucket: expect.any(Object) }))
    expect(wrapper.emitted('model')).toBeUndefined()
    await wrapper.getComponent(Select).get('button').trigger('click')
    const option = [...document.querySelectorAll<HTMLElement>('[role="option"]')].find(element => element.textContent?.includes('By model'))!
    option.click()
    await wrapper.vm.$nextTick()
    await wrapper.findAll('tbody tr')[0].get('td button').trigger('click')
    expect(wrapper.emitted('model')?.[0]?.[1]).toBe(overview.items[0].models[0].model)
    wrapper.unmount()
  })

  it('selects another trend group through the native selector', async () => {
    const overview = getMonitorPreviewScenario('normal').overview!
    const wrapper = mount(MonitorTrendPanel, { props: { items: overview.items, coverage: overview.coverage }, attachTo: document.body })
    const select = wrapper.getComponent(Select)
    expect(select.props('modelValue')).toBe(`${overview.items[0].platform}:${overview.items[0].group_id}`)
    await select.get('button').trigger('click')
    const option = [...document.querySelectorAll<HTMLElement>('[role="option"]')].find(element => element.textContent?.includes(overview.items[1].group_name))!
    option.click()
    await wrapper.vm.$nextTick()
    expect(select.props('modelValue')).toBe(`${overview.items[1].platform}:${overview.items[1].group_id}`)
    wrapper.unmount()
  })

  it('supports keyboard view switching and one tab stop', async () => {
    const items = [{ value: 'cards', label: 'Cards' }, { value: 'matrix', label: 'Matrix' }, { value: 'trend', label: 'Trend' }]
    const wrapper = mount(MonitorViewSwitcher, { props: { modelValue: 'cards', items }, attachTo: document.body })
    const tabs = wrapper.findAll('[role="tab"]')
    expect(tabs.map(tab => tab.attributes('tabindex'))).toEqual(['0', '-1', '-1'])
    await tabs[0].trigger('keydown', { key: 'ArrowRight' })
    expect(wrapper.emitted('update:modelValue')).toEqual([['matrix']])
    expect(document.activeElement).toBe(tabs[1].element)
    await wrapper.setProps({ modelValue: 'matrix' })
    await tabs[1].trigger('keydown', { key: 'End' })
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual(['trend'])
    wrapper.unmount()
  })
})
