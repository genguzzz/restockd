// normalizeSettings 与重复锁单识别
import { describe, expect, it } from 'vitest'
import { duplicateOrderOf } from '@/composables/useOrderPlacer'
import { ApiError } from './client'
import { normalizeSettings } from './index'
import type { Settings } from './types'

describe('normalizeSettings', () => {
  it('fills Go null slices', () => {
    const raw = { egress: null, notify_events: null } as unknown as Settings
    const settings = normalizeSettings(raw)
    expect(settings.egress).toEqual({ proxies: [], default: [] })
    expect(settings.notify_events).toEqual([])
    const partial = normalizeSettings({ egress: { proxies: null, default: ['direct'] }, notify_events: ['restock'] } as unknown as Settings)
    expect(partial.egress).toEqual({ proxies: [], default: ['direct'] })
    expect(partial.notify_events).toEqual(['restock'])
  })
})

describe('duplicateOrderOf', () => {
  it('extracts the existing order from a 409', () => {
    const order = { created_at: '2026-11-24T07:00:00Z', result: { pay_url: 'https://x/pay', invoice_id: '42' } }
    expect(duplicateOrderOf(new ApiError(409, 'dup', { error: 'dup', order }))).toEqual({
      at: order.created_at,
      payUrl: 'https://x/pay',
      invoiceId: '42',
    })
    expect(duplicateOrderOf(new ApiError(409, 'dup', order))?.invoiceId).toBe('42')
  })
  it('ignores other errors', () => {
    expect(duplicateOrderOf(new ApiError(409, 'in flight', { error: 'in flight' }))).toBeNull()
    expect(duplicateOrderOf(new ApiError(502, 'x', { order: { created_at: 'x' } }))).toBeNull()
    expect(duplicateOrderOf(new Error('x'))).toBeNull()
  })
})
