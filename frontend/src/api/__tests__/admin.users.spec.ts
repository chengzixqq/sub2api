import { beforeEach, describe, expect, it, vi } from 'vitest'

const { post, put } = vi.hoisted(() => ({
  post: vi.fn(),
  put: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: {
    post,
    put,
  },
}))

import {
  batchUpdateLimits,
  bindUserAuthIdentity,
  createAdjustmentIdempotencyKey,
  updateBalance,
  updateConcurrency,
  type AdminBindAuthIdentityRequest,
  type AdminBoundAuthIdentity,
  type BatchUpdateUserLimitsRequest,
  type BatchUpdateUserLimitsResponse,
} from '@/api/admin/users'
import type { UpdateUserRequest } from '@/types'

type Assert<T extends true> = T
type IsExact<T, U> = (
  (<G>() => G extends T ? 1 : 2) extends (<G>() => G extends U ? 1 : 2)
    ? ((<G>() => G extends U ? 1 : 2) extends (<G>() => G extends T ? 1 : 2) ? true : false)
    : false
)

type ExpectedAdminBindAuthIdentityRequest = {
  provider_type: string
  provider_key: string
  provider_subject: string
  issuer?: string
  metadata?: Record<string, unknown>
  channel?: {
    channel: string
    channel_app_id: string
    channel_subject: string
    metadata?: Record<string, unknown>
  }
}

type ExpectedAdminBoundAuthIdentity = {
  user_id: number
  provider_type: string
  provider_key: string
  provider_subject: string
  verified_at?: string | null
  issuer?: string | null
  metadata: Record<string, unknown> | null
  created_at: string
  updated_at: string
  channel?: {
    channel: string
    channel_app_id: string
    channel_subject: string
    metadata: Record<string, unknown> | null
    created_at: string
    updated_at: string
  } | null
}

const requestContractExact: Assert<
  IsExact<AdminBindAuthIdentityRequest, ExpectedAdminBindAuthIdentityRequest>
> = true
const responseContractExact: Assert<
  IsExact<AdminBoundAuthIdentity, ExpectedAdminBoundAuthIdentity>
> = true
const batchRequestContractExact: Assert<
  IsExact<
    BatchUpdateUserLimitsRequest,
    {
      user_ids: number[]
      all?: boolean
      concurrency?: number
      rpm_limit?: number
      notes?: string
    }
  >
> = true
const batchResponseContractExact: Assert<
  IsExact<BatchUpdateUserLimitsResponse, { affected: number }>
> = true

describe('admin users api auth identity binding', () => {
  beforeEach(() => {
    post.mockReset()
    put.mockReset()
  })

  it('posts the backend-compatible auth identity bind payload and returns the backend response shape', async () => {
    const payload: AdminBindAuthIdentityRequest = {
      provider_type: 'wechat',
      provider_key: 'wechat-main',
      provider_subject: 'union-123',
      metadata: { source: 'admin-repair' },
      channel: {
        channel: 'open',
        channel_app_id: 'wx-open',
        channel_subject: 'openid-123',
        metadata: { scene: 'migration' },
      },
    }

    const response: AdminBoundAuthIdentity = {
      user_id: 9,
      provider_type: 'wechat',
      provider_key: 'wechat-main',
      provider_subject: 'union-123',
      verified_at: '2026-04-22T00:00:00Z',
      issuer: null,
      metadata: { source: 'admin-repair' },
      created_at: '2026-04-22T00:00:00Z',
      updated_at: '2026-04-22T00:00:00Z',
      channel: {
        channel: 'open',
        channel_app_id: 'wx-open',
        channel_subject: 'openid-123',
        metadata: { scene: 'migration' },
        created_at: '2026-04-22T00:00:00Z',
        updated_at: '2026-04-22T00:00:00Z',
      },
    }
    post.mockResolvedValue({ data: response })

    const result = await bindUserAuthIdentity(9, payload)

    expect(post).toHaveBeenCalledWith('/admin/users/9/auth-identities', payload)
    expect(result).toEqual(response)
  })

  it('keeps bind auth identity request and response types aligned with the backend contract', () => {
    expect(requestContractExact).toBe(true)
    expect(responseContractExact).toBe(true)
  })

  it('posts batch limit updates once with only the supplied limit fields', async () => {
    const request: BatchUpdateUserLimitsRequest = {
      user_ids: [4, 7],
      all: false,
      rpm_limit: 0,
      notes: 'planned limit update',
    }
    post.mockResolvedValue({ data: { affected: 2 } satisfies BatchUpdateUserLimitsResponse })

    const result = await batchUpdateLimits(request)

    expect(post).toHaveBeenCalledWith('/admin/users/batch-limits', request, {
      headers: { 'Idempotency-Key': expect.any(String) },
    })
    expect(result).toEqual({ affected: 2 })
    expect(batchRequestContractExact).toBe(true)
    expect(batchResponseContractExact).toBe(true)
  })

  it('sends a concurrency adjustment note through the typed user update request', async () => {
    const typedRequest: UpdateUserRequest = {
      concurrency: 6,
      adjustment_notes: 'capacity correction',
    }
    put.mockResolvedValue({ data: { id: 9 } })

    await updateConcurrency(9, typedRequest.concurrency!, typedRequest.adjustment_notes)

    expect(put).toHaveBeenCalledWith('/admin/users/9', typedRequest, {
      headers: { 'Idempotency-Key': expect.any(String) },
    })
  })

  it('reuses an explicitly owned single-user concurrency key after an uncertain failure', async () => {
    const options = { idempotencyKey: createAdjustmentIdempotencyKey() }
    put.mockRejectedValueOnce(new Error('network result unknown'))

    await expect(updateConcurrency(11, 8, 'capacity correction', options)).rejects.toThrow('network result unknown')
    const firstHeaders = put.mock.calls[0][2].headers

    put.mockResolvedValueOnce({ data: { id: 11, concurrency: 8 } })
    await updateConcurrency(11, 8, 'capacity correction', options)

    expect(put.mock.calls[1][2].headers).toEqual(firstHeaders)
    expect(firstHeaders['Idempotency-Key']).toMatch(/^admin-adjustment-/)
  })

  it('reuses an explicitly owned balance adjustment key after an uncertain failure', async () => {
    const options = { idempotencyKey: createAdjustmentIdempotencyKey() }
    post.mockRejectedValueOnce(new Error('network result unknown'))

    await expect(updateBalance(9, 5, 'add', 'manual correction', options)).rejects.toThrow('network result unknown')
    const firstHeaders = post.mock.calls[0][2].headers

    post.mockResolvedValueOnce({ data: { id: 9, balance: 15 } })
    await updateBalance(9, 5, 'add', 'manual correction', options)

    expect(post.mock.calls[1][2].headers).toEqual(firstHeaders)
    expect(firstHeaders['Idempotency-Key']).toMatch(/^admin-adjustment-/)
  })

  it('uses distinct keys for independent concurrent adjustments with identical payloads', async () => {
    post.mockResolvedValue({ data: { id: 9, balance: 15 } })

    await Promise.all([
      updateBalance(9, 5, 'add', 'manual correction'),
      updateBalance(9, 5, 'add', 'manual correction'),
    ])

    const firstKey = post.mock.calls[0][2].headers['Idempotency-Key']
    const secondKey = post.mock.calls[1][2].headers['Idempotency-Key']
    expect(firstKey).toMatch(/^admin-adjustment-/)
    expect(secondKey).toMatch(/^admin-adjustment-/)
    expect(secondKey).not.toBe(firstKey)
  })
})
