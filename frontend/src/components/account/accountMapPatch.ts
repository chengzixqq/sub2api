type AccountMap = Record<string, unknown>

const equalJsonValue = (left: unknown, right: unknown): boolean => {
  if (Object.is(left, right)) return true
  if (Array.isArray(left) || Array.isArray(right)) {
    return Array.isArray(left) && Array.isArray(right) &&
      left.length === right.length && left.every((value, index) => equalJsonValue(value, right[index]))
  }
  if (left === null || right === null || typeof left !== 'object' || typeof right !== 'object') {
    return false
  }
  const leftRecord = left as AccountMap
  const rightRecord = right as AccountMap
  const leftKeys = Object.keys(leftRecord)
  const rightKeys = Object.keys(rightRecord)
  return leftKeys.length === rightKeys.length &&
    leftKeys.every((key) => Object.prototype.hasOwnProperty.call(rightRecord, key) && equalJsonValue(leftRecord[key], rightRecord[key]))
}

export const buildAccountMapPatch = (
  baseline: AccountMap | null | undefined,
  desired: AccountMap | null | undefined
): AccountMap => {
  if (desired === undefined) return {}

  const before = baseline ?? {}
  const after = desired ?? {}
  const patch = Object.create(null) as AccountMap
  const keys = new Set([...Object.keys(before), ...Object.keys(after)])
  for (const key of keys) {
    const hadKey = Object.prototype.hasOwnProperty.call(before, key)
    const hasKey = Object.prototype.hasOwnProperty.call(after, key) && after[key] !== undefined
    if (!hasKey) {
      if (hadKey) patch[key] = null
      continue
    }
    if (!hadKey || !equalJsonValue(before[key], after[key])) {
      patch[key] = after[key]
    }
  }
  return patch
}
