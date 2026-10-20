import { describe, expect, it } from 'vitest'
import type { LocationQuery } from 'vue-router'
import {
  ACCOUNT_DEEP_LINK_INVALID_DISPLAY_MAX,
  ACCOUNT_DEEP_LINK_SEARCH_MAX,
  parseAccountDeepLink
} from '../accountDeepLink'

describe('parseAccountDeepLink', () => {
  it('parses a valid edit id and strips it from the remaining query without mutating input', () => {
    const query: LocationQuery = { edit: '12', foo: 'bar' }
    const snapshot = JSON.parse(JSON.stringify(query))

    const link = parseAccountDeepLink(query)

    expect(link.present).toBe(true)
    expect(link.editId).toBe(12)
    expect(link.invalidEdit).toBeNull()
    expect(link.search).toBeNull()
    expect(link.rest).toEqual({ foo: 'bar' })
    expect(query).toEqual(snapshot)
    expect(link.rest).not.toBe(query)
  })

  it('accepts leading zeros and surrounding whitespace around digits', () => {
    expect(parseAccountDeepLink({ edit: '007' }).editId).toBe(7)
    expect(parseAccountDeepLink({ edit: ' 12 ' }).editId).toBe(12)
  })

  it.each(['abc', '0', '-3', '1.5', ' 12a', '1e3', '0x10', '99999999999999999999'])(
    'rejects invalid edit value %s',
    (raw) => {
      const link = parseAccountDeepLink({ edit: raw })

      expect(link.present).toBe(true)
      expect(link.editId).toBeNull()
      expect(link.invalidEdit).toBe(raw)
      expect(link.rest).toEqual({})
    }
  )

  it('truncates an oversized invalid edit value for display', () => {
    const raw = 'x'.repeat(500)

    const link = parseAccountDeepLink({ edit: raw })

    expect(link.editId).toBeNull()
    expect(link.invalidEdit).toBe(`${'x'.repeat(ACCOUNT_DEEP_LINK_INVALID_DISPLAY_MAX)}…`)
  })

  it('takes the first non-empty value from array query values', () => {
    const link = parseAccountDeepLink({ edit: [null, '', '7', '9'], search: ['', ' acc '] })

    expect(link.editId).toBe(7)
    expect(link.search).toBe('acc')
    expect(link.present).toBe(true)
  })

  it('treats empty edit and blank search as absent but still strips them', () => {
    const link = parseAccountDeepLink({ edit: '', search: '   ', page: '2' })

    expect(link.present).toBe(false)
    expect(link.editId).toBeNull()
    expect(link.invalidEdit).toBeNull()
    expect(link.search).toBeNull()
    expect(link.rest).toEqual({ page: '2' })
  })

  it('treats a bare edit key without value as absent', () => {
    const link = parseAccountDeepLink({ edit: null })

    expect(link.present).toBe(false)
    expect(link.editId).toBeNull()
    expect(link.invalidEdit).toBeNull()
  })

  it('trims search and truncates it to the backend limit', () => {
    const long = `  ${'s'.repeat(ACCOUNT_DEEP_LINK_SEARCH_MAX + 20)}  `

    const link = parseAccountDeepLink({ search: long })

    expect(ACCOUNT_DEEP_LINK_SEARCH_MAX).toBe(100)
    expect(link.present).toBe(true)
    expect(link.editId).toBeNull()
    expect(link.invalidEdit).toBeNull()
    expect(link.search).toBe('s'.repeat(ACCOUNT_DEEP_LINK_SEARCH_MAX))
  })

  it('reports no deep link when neither key is present', () => {
    expect(parseAccountDeepLink({})).toEqual({
      present: false,
      editId: null,
      invalidEdit: null,
      search: null,
      rest: {}
    })
    const other: LocationQuery = { page: '2' }
    const link = parseAccountDeepLink(other)
    expect(link.present).toBe(false)
    expect(link.rest).toEqual({ page: '2' })
  })
})
