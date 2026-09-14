import { effectScope, ref } from 'vue'
import { flushPromises } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { useObservationOverview } from '../useObservationOverview'
import type { ObservationOverview } from '@/api/channelMonitorV2'

const api = vi.hoisted(() => ({ getObservationOverview: vi.fn() }))
vi.mock('@/api/channelMonitorV2', () => api)
const snapshot = (range: string) => ({ source: 'terminal_v1', contract_version: 2, coverage: { requested_start: range }, items: [] }) as unknown as ObservationOverview

describe('observation request lifetime', () => {
  it('drops old responses, clears different ranges and sends an immutable query', async () => {
    const pending: Array<(value: ObservationOverview) => void> = []
    api.getObservationOverview.mockImplementation(() => new Promise(resolve => pending.push(resolve)))
    const filter = ref({ range: '24h' as const, platforms: [] as string[], groupIds: [] as number[], models: [] as string[] })
    const scope = effectScope()
    const state = scope.run(() => useObservationOverview(filter, ref(false), ref(false)))!
    const old = state.load()
    const oldFilter = api.getObservationOverview.mock.calls[0]![0]
    filter.value.platforms.push('openai')
    const fresh = state.load()
    expect(oldFilter.platforms).toEqual([])
    expect(api.getObservationOverview.mock.calls[0]![2].aborted).toBe(true)
    pending[1]!(snapshot('new'))
    await fresh
    pending[0]!(snapshot('old'))
    await old
    expect(state.data.value?.coverage.requested_start).toBe('new')
    scope.stop()
  })

  it('clears obsolete values on error and aborts on disposal', async () => {
    api.getObservationOverview.mockResolvedValueOnce(snapshot('initial'))
    const filter = ref({ range: '24h' as const, platforms: [] as string[], groupIds: [] as number[], models: [] as string[] })
    const scope = effectScope()
    const state = scope.run(() => useObservationOverview(filter, ref(false), ref(false)))!
    await state.load()
    api.getObservationOverview.mockRejectedValueOnce(new Error('offline'))
    filter.value.platforms = ['openai']
    await state.load()
    expect(state.data.value).toBeNull()
    expect(state.error.value).toBeTruthy()
    api.getObservationOverview.mockImplementationOnce(() => new Promise(() => {}))
    void state.load()
    await flushPromises()
    scope.stop()
    expect(api.getObservationOverview.mock.lastCall![2].aborted).toBe(true)
  })
})
