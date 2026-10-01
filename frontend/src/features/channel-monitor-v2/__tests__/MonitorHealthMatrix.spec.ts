import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import MonitorHealthMatrix from '../MonitorHealthMatrix.vue'
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
    await wrapper.get('select').setValue('time')
    const first = wrapper.findAll('tbody tr')[0].findAll('td button')[0]
    expect(first.attributes('aria-label')).not.toContain('states.missing')
    await first.trigger('click')
    expect(wrapper.emitted('bucket')?.[0]?.[1]).toEqual(expect.objectContaining({ bucket: expect.any(Object) }))
    wrapper.unmount()
  })
})
