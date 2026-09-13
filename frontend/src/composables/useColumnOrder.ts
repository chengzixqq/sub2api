import { computed, ref, toValue, watch, type MaybeRefOrGetter } from 'vue'
import type { Column } from '@/components/common/types'

const pinnedStart = new Set(['select', 'selection'])
const pinnedEnd = new Set(['actions'])

export function columnOrderStorageKey(
  table: string | undefined,
  userId: number | null | undefined,
  role: string | undefined,
  workspaceId?: number | null
): string | null {
  if (!table || userId == null) return null
  return `sub2api:column-order:v1:${JSON.stringify([userId, role ?? 'user', workspaceId ?? null, table])}`
}

export function isMovableColumn(key: string): boolean {
  return !pinnedStart.has(key) && !pinnedEnd.has(key)
}

function reconcileOrder(saved: string[], defaults: string[]): string[] {
  // Keep unseen keys so hiding a column does not erase its preferred position.
  const result = [...new Set(saved.filter(isMovableColumn))]
  for (const [index, key] of defaults.entries()) {
    if (result.includes(key)) continue
    const previous = defaults.slice(0, index).reverse().find(candidate => result.includes(candidate))
    const next = defaults.slice(index + 1).find(candidate => result.includes(candidate))
    const at = previous ? result.indexOf(previous) + 1 : next ? result.indexOf(next) : result.length
    result.splice(at, 0, key)
  }
  return result
}

export function useColumnOrder(
  columns: MaybeRefOrGetter<Column[]>,
  storageKey: MaybeRefOrGetter<string | null>,
  allColumns: MaybeRefOrGetter<Column[]> = columns
) {
  const order = ref<string[]>([])
  const defaultKeys = computed(() => toValue(allColumns).map(column => column.key).filter(isMovableColumn))

  watch(() => toValue(storageKey), (key) => {
    let saved: string[] = []
    if (key) {
      try {
        const parsed: unknown = JSON.parse(localStorage.getItem(key) ?? 'null')
        if (Array.isArray(parsed) && parsed.length <= 500) {
          saved = parsed.filter((value): value is string => typeof value === 'string' && value.length <= 256)
        }
      } catch { /* Browser storage is optional. */ }
    }
    order.value = reconcileOrder(saved, defaultKeys.value)
  }, { immediate: true, flush: 'sync' })

  watch(defaultKeys, (keys) => { order.value = reconcileOrder(order.value, keys) }, { flush: 'sync' })

  const orderedColumns = computed(() => {
    const source = toValue(columns)
    const lookup = new Map(source.map(column => [column.key, column]))
    return [
      ...source.filter(column => pinnedStart.has(column.key)),
      ...order.value.flatMap(key => lookup.has(key) ? [lookup.get(key)!] : []),
      ...source.filter(column => pinnedEnd.has(column.key))
    ]
  })
  const movableColumns = computed(() => orderedColumns.value.filter(column => isMovableColumn(column.key)))

  function move(key: string, target: string, side: 'before' | 'after' = 'before') {
    if (key === target || !defaultKeys.value.includes(key) || !defaultKeys.value.includes(target)) return
    const next = order.value.filter(candidate => candidate !== key)
    const targetIndex = next.indexOf(target)
    if (targetIndex < 0) return
    next.splice(targetIndex + (side === 'after' ? 1 : 0), 0, key)
    order.value = next
    const storage = toValue(storageKey)
    if (storage) {
      try { localStorage.setItem(storage, JSON.stringify(next)) } catch { /* Keep the session preference. */ }
    }
  }

  function moveBy(key: string, offset: -1 | 1) {
    const visible = movableColumns.value.map(column => column.key)
    const target = visible[visible.indexOf(key) + offset]
    if (target) move(key, target, offset < 0 ? 'before' : 'after')
  }

  function reset() {
    order.value = [...defaultKeys.value]
    const storage = toValue(storageKey)
    if (storage) {
      try { localStorage.removeItem(storage) } catch { /* Keep the session preference. */ }
    }
  }

  return { orderedColumns, movableColumns, move, moveBy, reset }
}
