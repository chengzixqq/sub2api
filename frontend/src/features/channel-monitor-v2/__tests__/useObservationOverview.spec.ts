import { effectScope, ref } from 'vue'
import { flushPromises } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { useObservationOverview } from '../useObservationOverview'
import type { ObservationOverview } from '@/api/channelMonitorV2'

const api = vi.hoisted(() => ({ getObservationOverview: vi.fn() }))
vi.mock('@/api/channelMonitorV2', () => api)
const snapshot = (range: string) => ({ source: 'terminal_v1', contract_version: 2, coverage: { requested_start: range }, items: [] }) as unknown as ObservationOverview

describe('observation request lifetime', () => {
	it('reloads with the administrator projection when the role becomes available', async () => {
		api.getObservationOverview.mockResolvedValue(snapshot('admin'))
		const filter = ref({ range: '24h' as const, platforms: [] as string[], groupIds: [] as number[], models: [] as string[] })
		const admin = ref(false)
		const scope = effectScope()
		const state = scope.run(() => useObservationOverview(filter, admin, ref(false)))!
		await state.load()
		expect(api.getObservationOverview.mock.calls.at(-1)![1]).toBe(false)
		admin.value = true
		await state.load()
		expect(api.getObservationOverview.mock.calls.at(-1)![1]).toBe(true)
		scope.stop()
	})

	it('advances the snapshot boundary for refreshes', async () => {
		vi.useFakeTimers()
		vi.setSystemTime(new Date('2026-09-15T06:00:00.000Z'))
		api.getObservationOverview.mockResolvedValue(snapshot('initial'))
		const filter = ref({ range: '24h' as const, platforms: [] as string[], groupIds: [] as number[], models: [] as string[] })
		const scope = effectScope()
		const state = scope.run(() => useObservationOverview(filter, ref(false), ref(false)))!
		await state.load()
		const firstEnd = api.getObservationOverview.mock.calls.at(-1)![3].endTime
		vi.setSystemTime(new Date('2026-09-15T06:01:00.000Z'))
		await state.load(true)
		const refreshedEnd = api.getObservationOverview.mock.calls.at(-1)![3].endTime
		expect(refreshedEnd).not.toBe(firstEnd)
		expect(refreshedEnd).toBe('2026-09-15T06:01:00.000Z')
		scope.stop()
		vi.useRealTimers()
	})

	it('preserves the last snapshot and marks it stale when a same-scope refresh fails', async () => {
		api.getObservationOverview.mockResolvedValueOnce(snapshot('initial'))
		const filter = ref({ range: '24h' as const, platforms: [] as string[], groupIds: [] as number[], models: [] as string[] })
		const scope = effectScope()
		const state = scope.run(() => useObservationOverview(filter, ref(false), ref(false)))!
		await state.load()
		api.getObservationOverview.mockRejectedValueOnce(new Error('offline'))
		await state.load(true, true)
		expect(state.data.value?.coverage.requested_start).toBe('initial')
		expect(state.error.value).toBe(true)
		expect(state.stale.value).toBe(true)
		api.getObservationOverview.mockImplementationOnce(() => new Promise(() => undefined))
		void state.load(true)
		expect(state.stale.value).toBe(true)
		scope.stop()
	})

  it.each(['compact', 'legacy', 'mixed'])('accepts the unified %s source', async (source) => {
    api.getObservationOverview.mockResolvedValueOnce({ ...snapshot('unified'), source })
    const filter = ref({ range: '24h' as const, platforms: [], groupIds: [], models: [] })
    const scope = effectScope()
    const state = scope.run(() => useObservationOverview(filter, ref(true), ref(false)))!
    await state.load()
    expect(state.error.value).toBe(false)
    expect(state.data.value?.source).toBe(source)
    scope.stop()
  })

  it('revokes cached owner data on an authorization failure even without a local role change', async () => {
    api.getObservationOverview.mockResolvedValueOnce(snapshot('owner'))
    const filter = ref({ range: '24h' as const, platforms: [], groupIds: [], models: [] })
    const scope = effectScope()
    const state = scope.run(() => useObservationOverview(filter, ref(true), ref(false)))!
    await state.load()
    api.getObservationOverview.mockRejectedValueOnce({ response: { status: 403 } })
    await state.load(true)
    expect(state.data.value).toBeNull()
    expect(state.errorStatus.value).toBe(403)
    scope.stop()
  })

  it('immediately removes another workspace snapshot before loading its replacement', async () => {
    api.getObservationOverview.mockResolvedValueOnce(snapshot('private'))
    const filter = ref({ range: '24h' as const, platforms: [], groupIds: [], models: [] })
    const identity = ref('user-1:workspace-1')
    const scope = effectScope()
    const state = scope.run(() => useObservationOverview(filter, ref(true), ref(false), identity))!
    await state.load()
    identity.value = 'user-1:workspace-2'
    expect(state.data.value).toBeNull()
    scope.stop()
  })

	it('cancels an in-flight request when the view switches to legacy diagnostics', async () => {
		const pending = new Promise<ObservationOverview>(() => undefined)
		api.getObservationOverview.mockReturnValueOnce(pending)
		const filter = ref({ range: '24h' as const, platforms: [] as string[], groupIds: [] as number[], models: [] as string[] })
		const scope = effectScope()
		const state = scope.run(() => useObservationOverview(filter, ref(true), ref(false)))!
		void state.load()
		await flushPromises()
		expect(state.loading.value).toBe(true)
		state.cancel()
		expect(state.loading.value).toBe(false)
		expect(api.getObservationOverview.mock.lastCall?.[2].aborted).toBe(true)
		scope.stop()
	})

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
