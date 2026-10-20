import type { LocationQuery, LocationQueryValue } from 'vue-router'

/** 账号管理页深链：`/admin/accounts?edit=<id>[&search=<名称>]`（App“在 Web 中编辑”使用）。 */
export const ACCOUNT_DEEP_LINK_EDIT_KEY = 'edit'
export const ACCOUNT_DEEP_LINK_SEARCH_KEY = 'search'
/** 后端 GET /admin/accounts 按 UTF-8 字节把 search 截到 100（`search[:100]`），这里用同一上限。 */
export const ACCOUNT_DEEP_LINK_SEARCH_MAX_BYTES = 100
/** 非法 edit 值回显到提示里时的最大长度，避免超长链接撑爆 toast。 */
export const ACCOUNT_DEEP_LINK_INVALID_DISPLAY_MAX = 32

const POSITIVE_INTEGER_PATTERN = /^\d+$/

export interface AccountDeepLink {
  /** edit 或 search 至少有一个非空值。 */
  present: boolean
  /** 合法的账号 ID（大于 0 的安全整数）；未提供或非法时为 null。 */
  editId: number | null
  /** 提供了 edit 但不合法时的原值（过长会截断），用于提示。 */
  invalidEdit: string | null
  /** 去掉首尾空白并截断后的搜索词；未提供或为空时为 null。 */
  search: string | null
  /** 去掉 edit 与 search 后的其余 query（新对象，不修改入参）。 */
  rest: LocationQuery
}

const firstNonEmptyString = (value: LocationQueryValue | LocationQueryValue[] | undefined): string | null => {
  if (Array.isArray(value)) {
    return value.find((item): item is string => typeof item === 'string' && item.length > 0) ?? null
  }
  return typeof value === 'string' && value.length > 0 ? value : null
}

const parseEditId = (raw: string): number | null => {
  const trimmed = raw.trim()
  if (!POSITIVE_INTEGER_PATTERN.test(trimmed)) return null
  const id = Number(trimmed)
  return Number.isSafeInteger(id) && id > 0 ? id : null
}

const truncateForDisplay = (raw: string): string =>
  raw.length > ACCOUNT_DEEP_LINK_INVALID_DISPLAY_MAX ? `${raw.slice(0, ACCOUNT_DEEP_LINK_INVALID_DISPLAY_MAX)}…` : raw

const utf8ByteLength = (codePoint: number): number =>
  codePoint <= 0x7f ? 1 : codePoint <= 0x7ff ? 2 : codePoint <= 0xffff ? 3 : 4

/**
 * 按完整字符截到 maxBytes 个 UTF-8 字节以内。
 * 不能用 String#slice：按 UTF-16 码元截断会把 emoji 等代理对劈开，
 * 留下的孤立代理会让 encodeURIComponent 抛 URIError，列表请求全部失败。
 */
const truncateUtf8 = (value: string, maxBytes: number): string => {
  let bytes = 0
  let out = ''
  for (const char of value) {
    bytes += utf8ByteLength(char.codePointAt(0) ?? 0)
    if (bytes > maxBytes) break
    out += char
  }
  return out
}

const normalizeSearch = (raw: string | null): string | null => {
  if (raw === null) return null
  const trimmed = truncateUtf8(raw.trim(), ACCOUNT_DEEP_LINK_SEARCH_MAX_BYTES)
  return trimmed.length > 0 ? trimmed : null
}

/** 解析账号页深链参数。纯函数，不读写路由。 */
export function parseAccountDeepLink(query: LocationQuery): AccountDeepLink {
  const {
    [ACCOUNT_DEEP_LINK_EDIT_KEY]: rawEditValue,
    [ACCOUNT_DEEP_LINK_SEARCH_KEY]: rawSearchValue,
    ...rest
  } = query

  const rawEdit = firstNonEmptyString(rawEditValue)
  const editId = rawEdit === null ? null : parseEditId(rawEdit)
  const invalidEdit = rawEdit !== null && editId === null ? truncateForDisplay(rawEdit) : null
  const search = normalizeSearch(firstNonEmptyString(rawSearchValue))

  return {
    present: rawEdit !== null || search !== null,
    editId,
    invalidEdit,
    search,
    rest
  }
}
