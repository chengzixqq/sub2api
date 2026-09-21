import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import ObservationCurrentStatus from '../ObservationCurrentStatus.vue'
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, te: () => false, locale: { value: 'en' } }) }))
describe('current channel status', () => {
  it('shows probe evidence separately from traffic', () => {
    const wrapper = mount(ObservationCurrentStatus, { props: { status: { state: 'healthy', source: 'probe', reason: 'probe_success' } } })
    expect(wrapper.text()).toContain('unified.states.healthy')
    expect(wrapper.text()).toContain('unified.sources.probe')
    expect(wrapper.text()).not.toContain('unified.sources.traffic')
  })
  it('does not represent a retained healthy snapshot as current after refresh failure', () => {
    const wrapper = mount(ObservationCurrentStatus, { props: { status: { state: 'healthy', source: 'traffic', reason: '' }, stale: true } })
    expect(wrapper.text()).toContain('unified.states.stale')
    expect(wrapper.text()).not.toContain('unified.states.healthy')
  })
  it('shows unknown when current evidence is absent', () => {
    const wrapper = mount(ObservationCurrentStatus)
    expect(wrapper.text()).toContain('unified.states.unknown')
  })
})
