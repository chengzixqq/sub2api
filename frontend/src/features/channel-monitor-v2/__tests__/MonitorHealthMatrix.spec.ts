import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import MonitorHealthMatrix from '../MonitorHealthMatrix.vue'
import Select from '@/components/common/Select.vue'
import { getMonitorPreviewScenario } from '../monitorPreview'

vi.mock('vue-i18n', async importOriginal => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key, te: () => false, locale: { value: 'en' } }) }))

describe('monitor traffic matrix', () => {
  it('separates absent, no-sample, insufficient, healthy and critical cells', async () => {
    const overview = getMonitorPreviewScenario('normal').overview!
    const item = overview.items[0]
    const original = item.models[0]
    item.models = [
      original,
      { ...original, model: 'no-samples', traffic: { ...original.metrics, sample_state: 'no_samples' } },
      { ...original, model: 'insufficient', traffic: { ...original.metrics, sample_state: 'insufficient' } },
      { ...original, model: 'critical', health: { reliability: 'healthy', latency: 'critical' } },
    ]
    const wrapper = mount(MonitorHealthMatrix, { props: { items: overview.items, coverage: overview.coverage } })
    wrapper.getComponent(Select).vm.$emit('update:modelValue', 'model')
    await wrapper.vm.$nextTick()
    const row = wrapper.findAll('tbody tr')[0]
    const cells = row.findAll('td button')
    expect(cells.map(cell => cell.attributes('aria-label'))).toEqual(expect.arrayContaining([
      expect.stringContaining('states.healthy'), expect.stringContaining('states.no_samples'), expect.stringContaining('states.insufficient'), expect.stringContaining('states.critical'), expect.stringContaining('states.missing'),
    ]))
    const missing = cells.find(cell => cell.attributes('aria-label')?.includes('states.missing'))!
    await missing.trigger('click')
    expect(wrapper.emitted('model')).toBeUndefined()
    wrapper.unmount()
  })

  it('matches equivalent timestamp representations without inventing a gap', async () => {
    const overview = getMonitorPreviewScenario('normal').overview!
    overview.items[0].buckets.forEach(bucket => { bucket.bucket_start = bucket.bucket_start.replace('.000Z', 'Z') })
    const wrapper = mount(MonitorHealthMatrix, { props: { items: overview.items, coverage: overview.coverage } })
    expect(wrapper.getComponent(Select).props('modelValue')).toBe('time')
    const first = wrapper.findAll('tbody tr')[0].findAll('td button')[0]
    expect(first.attributes('aria-label')).not.toContain('states.missing')
    await first.trigger('click')
    expect(wrapper.emitted('bucket')?.[0]?.[1]).toEqual(expect.objectContaining({ bucket: expect.any(Object) }))
    wrapper.unmount()
  })

  it('uses model cells when time coverage is absent, then defaults to time when coverage arrives', async () => {
    const overview = getMonitorPreviewScenario('normal').overview!
    const wrapper = mount(MonitorHealthMatrix, { props: { items: overview.items } })
    expect(wrapper.getComponent(Select).props('modelValue')).toBe('model')
    await wrapper.findAll('tbody tr')[0].get('td button').trigger('click')
    expect(wrapper.emitted('model')?.[0]?.[1]).toBe(overview.items[0].models[0].model)
    await wrapper.setProps({ coverage: overview.coverage })
    expect(wrapper.getComponent(Select).props('modelValue')).toBe('time')
    await wrapper.findAll('tbody tr')[0].get('td button').trigger('click')
    expect(wrapper.emitted('bucket')?.[0]?.[1]).toEqual(expect.objectContaining({ bucket: expect.any(Object) }))
    wrapper.unmount()
  })
})
