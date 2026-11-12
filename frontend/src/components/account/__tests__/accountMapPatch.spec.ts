import { describe, expect, it } from 'vitest'
import { buildAccountMapPatch } from '../accountMapPatch'

describe('buildAccountMapPatch', () => {
  it('leaves maps untouched when the editor did not submit that map', () => {
    expect(buildAccountMapPatch({ api_key: 'redacted', runtime: 1 }, undefined)).toEqual({})
  })

  it('submits only changed top-level keys and preserves equivalent nested values', () => {
    const baseline = {
      api_key: 'redacted',
      base_url: 'https://old.example',
      model_mapping: { chat: ['a', 'b'] },
      quota_used: 10
    }
    const desired = {
      api_key: 'redacted',
      base_url: 'https://new.example',
      model_mapping: { chat: ['a', 'b'] },
      quota_used: 10
    }

    expect(buildAccountMapPatch(baseline, desired)).toEqual({ base_url: 'https://new.example' })
  })

  it('represents explicit removal without including keys hidden by redaction', () => {
    expect(buildAccountMapPatch(
      { base_url: 'https://api.example', retired: true },
      { base_url: 'https://api.example' }
    )).toEqual({ retired: null })
  })

  it('does not treat nested object key order as a change', () => {
    expect(buildAccountMapPatch(
      { settings: { first: 1, second: 2 } },
      { settings: { second: 2, first: 1 } }
    )).toEqual({})
  })

  it('preserves special JSON keys as own patch fields', () => {
    const baseline = JSON.parse('{"__proto__":{"before":true}}') as Record<string, unknown>
    const desired = JSON.parse('{"__proto__":{"after":true}}') as Record<string, unknown>
    const patch = buildAccountMapPatch(baseline, desired)

    expect(Object.prototype.hasOwnProperty.call(patch, '__proto__')).toBe(true)
    expect(patch['__proto__']).toEqual({ after: true })
  })
})
