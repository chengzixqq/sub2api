import { effectScope, nextTick, ref } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { columnOrderStorageKey, useColumnOrder } from '../useColumnOrder'

describe('useColumnOrder', () => {
  beforeEach(() => localStorage.clear())

  it('isolates preferences by user, role, workspace and table', () => {
    expect(columnOrderStorageKey('orders', null, 'admin')).toBeNull()
    const keys = [
      columnOrderStorageKey('orders', 1, 'admin'),
      columnOrderStorageKey('orders', 2, 'admin'),
      columnOrderStorageKey('orders', 1, 'user'),
      columnOrderStorageKey('orders', 1, 'admin', 8),
      columnOrderStorageKey('keys', 1, 'admin')
    ]
    expect(new Set(keys).size).toBe(keys.length)
  })

  it('moves columns without mutating the caller and keeps selection/actions pinned', () => {
    const scope = effectScope()
    scope.run(() => {
      const columns = ref(['select', 'id', 'user', 'amount', 'actions'].map(key => ({ key, label: key })))
      const order = useColumnOrder(columns, () => 'test')
      order.move('user', 'id', 'before')
      order.move('actions', 'id', 'before')
      expect(order.orderedColumns.value.map(c => c.key)).toEqual(['select', 'user', 'id', 'amount', 'actions'])
      expect(columns.value.map(c => c.key)).toEqual(['select', 'id', 'user', 'amount', 'actions'])
      expect(order.movableColumns.value.map(c => c.key)).toEqual(['user', 'id', 'amount'])
    })
    scope.stop()
  })

  it('retains hidden positions, inserts new columns by default order and restores defaults', async () => {
    const scope = effectScope()
    const columns = ref(['a', 'b', 'c'].map(key => ({ key, label: key })))
    const order = scope.run(() => useColumnOrder(columns, () => 'test'))!
    order.move('c', 'a', 'before')
    columns.value = ['a', 'c'].map(key => ({ key, label: key }))
    await nextTick()
    expect(order.orderedColumns.value.map(c => c.key)).toEqual(['c', 'a'])
    columns.value = ['a', 'b', 'new', 'c'].map(key => ({ key, label: key }))
    await nextTick()
    expect(order.orderedColumns.value.map(c => c.key)).toEqual(['c', 'a', 'b', 'new'])
    order.reset()
    expect(order.orderedColumns.value.map(c => c.key)).toEqual(['a', 'b', 'new', 'c'])
    expect(localStorage.getItem('test')).toBeNull()
    scope.stop()
  })

  it('reloads saved order on identity changes without leaking the previous account', async () => {
    const scope = effectScope()
    const identity = ref('first')
    const columns = ref(['a', 'b'].map(key => ({ key, label: key })))
    const order = scope.run(() => useColumnOrder(columns, identity))!
    order.move('b', 'a', 'before')
    identity.value = 'second'
    await nextTick()
    expect(order.orderedColumns.value.map(c => c.key)).toEqual(['a', 'b'])
    identity.value = 'first'
    await nextTick()
    expect(order.orderedColumns.value.map(c => c.key)).toEqual(['b', 'a'])
    scope.stop()
  })

  it('ignores invalid storage', () => {
    localStorage.setItem('test', '{bad json')
    const scope = effectScope()
    scope.run(() => {
      const order = useColumnOrder(() => [{ key: 'a', label: 'A' }], () => 'test')
      expect(order.orderedColumns.value.map(c => c.key)).toEqual(['a'])
    })
    scope.stop()
  })

  it('keeps the complete hidden schema across remounts', () => {
    const columns = ref(['a', 'c'].map(key => ({ key, label: key })))
    const allColumns = ref(['a', 'b', 'c'].map(key => ({ key, label: key })))
    const first = effectScope()
    first.run(() => useColumnOrder(columns, () => 'test', allColumns).move('c', 'a', 'before'))
    first.stop()
    const second = effectScope()
    second.run(() => {
      const order = useColumnOrder(allColumns, () => 'test', allColumns)
      expect(order.orderedColumns.value.map(column => column.key)).toEqual(['c', 'a', 'b'])
    })
    second.stop()
  })

  it('keeps working in memory when browser storage throws', () => {
    const get = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('unavailable') })
    const set = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('unavailable') })
    const remove = vi.spyOn(Storage.prototype, 'removeItem').mockImplementation(() => { throw new Error('unavailable') })
    const scope = effectScope()
    scope.run(() => {
      const order = useColumnOrder(() => ['a', 'b'].map(key => ({ key, label: key })), () => 'test')
      order.move('b', 'a', 'before')
      expect(order.orderedColumns.value.map(column => column.key)).toEqual(['b', 'a'])
      expect(() => order.reset()).not.toThrow()
    })
    scope.stop()
    get.mockRestore()
    set.mockRestore()
    remove.mockRestore()
  })
})
