// list / egress / labels / orderSpec / 时间工具单元测试
import { describe, expect, it } from 'vitest'
import type { Account, EgressProxy, Product } from '@/api/types'
import { DIRECT_EGRESS_ID, egressLabel, egressOptions, newEgressId } from './egress'
import { formatInterval, hasTime, isRecent } from './format'
import { accountStatusMeta, metaOf, notifyResultMeta } from './labels'
import { removeById, upsertById } from './list'
import { applyProductDefaults, defaultAccount, emptySpec, estimatePrice } from './orderSpec'

describe('upsertById / removeById', () => {
  const list = [{ id: 'a', v: 1 }, { id: 'b', v: 2 }]
  it('replaces in place', () => {
    expect(upsertById(list, { id: 'b', v: 3 })).toEqual([{ id: 'a', v: 1 }, { id: 'b', v: 3 }])
  })
  it('appends or prepends new items', () => {
    expect(upsertById(list, { id: 'c', v: 0 }).map((item) => item.id)).toEqual(['a', 'b', 'c'])
    expect(upsertById(list, { id: 'c', v: 0 }, { prepend: true }).map((item) => item.id)).toEqual(['c', 'a', 'b'])
  })
  it('does not mutate and removes by id', () => {
    upsertById(list, { id: 'a', v: 9 })
    expect(list[0]!.v).toBe(1)
    expect(removeById(list, 'a')).toEqual([{ id: 'b', v: 2 }])
  })
})

describe('egress', () => {
  const proxies: EgressProxy[] = [
    { id: 'p1', name: 'dmit LA', url: '', enabled: true },
    { id: 'p2', name: '', url: '', enabled: false },
  ]
  it('builds options with direct first and disabled proxies', () => {
    expect(egressOptions(proxies)).toEqual([
      { label: '直连（本服务器出口）', value: DIRECT_EGRESS_ID },
      { label: 'dmit LA', value: 'p1', disabled: false },
      { label: 'p2', value: 'p2', disabled: true },
    ])
  })
  it('labels ids', () => {
    expect(egressLabel(DIRECT_EGRESS_ID, proxies)).toBe('直连')
    expect(egressLabel('p1', proxies)).toBe('dmit LA')
    expect(egressLabel('gone', proxies)).toBe('gone')
  })
  it('generates unique ids avoiding direct and existing ones', () => {
    const queue = ['direct', 'p1', 'fresh']
    expect(newEgressId(proxies, () => queue.shift()!)).toBe('fresh')
    expect(newEgressId([])).toMatch(/^[0-9a-f]{8}$/)
  })
})

describe('labels', () => {
  it('falls back to the raw key', () => {
    expect(metaOf(accountStatusMeta, 'active')).toEqual({ label: '有效', type: 'success' })
    expect(metaOf(accountStatusMeta, 'weird')).toEqual({ label: 'weird', type: 'default' })
  })
  it('describes notify test results', () => {
    expect(notifyResultMeta('telegram', 'ok')).toEqual({ label: 'Telegram：成功', type: 'success' })
    expect(notifyResultMeta('email', 'disabled').type).toBe('default')
    expect(notifyResultMeta('bark', 'not_configured')).toEqual({ label: 'Bark：未配置完整', type: 'warning' })
    expect(notifyResultMeta('bark', 'not configured').type).toBe('warning')
    expect(notifyResultMeta('x', 'boom')).toEqual({ label: 'x：boom', type: 'error' })
  })
})

describe('time helpers', () => {
  const now = Date.parse('2026-11-24T08:00:00Z')
  it('hasTime rejects Go zero time', () => {
    expect(hasTime('0001-01-01T00:00:00Z')).toBe(false)
    expect(hasTime('')).toBe(false)
    expect(hasTime('2026-11-24T07:00:00Z')).toBe(true)
  })
  it('isRecent', () => {
    expect(isRecent('2026-11-24T07:00:00Z', 3600_000 * 2, now)).toBe(true)
    expect(isRecent('2026-11-22T07:00:00Z', 86400_000, now)).toBe(false)
    expect(isRecent('0001-01-01T00:00:00Z', Number.MAX_SAFE_INTEGER, now)).toBe(false)
  })
  it('formatInterval', () => {
    expect(formatInterval(90)).toBe('90 秒')
    expect(formatInterval(180)).toBe('3 分钟')
    expect(formatInterval(3600)).toBe('1 小时')
    expect(formatInterval(5400)).toBe('90 分钟')
  })
})

const money = (cents: number) => ({ cents, currency: 'CNY' })
const product: Product = {
  site_id: 's',
  id: 'p',
  name: 'VPS',
  specs: {},
  prices: [
    { cycle: 'monthly', amount: money(18800), setup: money(0) },
    { cycle: 'quarterly', amount: money(53580), setup: money(1000) },
  ],
  options: [
    { key: 'ip', label: 'IP', choices: [{ value: '1', label: '1 IP', delta_monthly: money(0) }, { value: '2', label: '2 IP', delta_monthly: money(500) }] },
  ],
  os_templates: [{ value: 'debian', label: 'Debian', delta_monthly: money(0) }],
  stock: { status: 'in_stock' },
  fetched_at: '',
  watched: false,
  last_placed_at: '0001-01-01T00:00:00Z',
}

describe('estimatePrice', () => {
  it('multiplies price, option deltas and setup by qty', () => {
    const estimate = estimatePrice(product, 'quarterly', { ip: '2' }, 2)
    expect(estimate?.base).toEqual(money(107160))
    expect(estimate?.extra).toEqual(money(500 * 3 * 2))
    expect(estimate?.total).toEqual(money(107160 + 3000 + 2000))
  })
  it('treats qty < 1 as 1 and returns null without a matching price', () => {
    expect(estimatePrice(product, 'monthly', {}, 0)?.total).toEqual(money(18800))
    expect(estimatePrice(product, 'annually', {}, 1)).toBeNull()
    expect(estimatePrice(null, 'monthly', {}, 1)).toBeNull()
  })
})

describe('applyProductDefaults', () => {
  it('fills cycle and options, keeps valid values, drops unknown OS', () => {
    const filled = applyProductDefaults({ ...emptySpec(), os: 'centos' }, product)
    expect(filled).toMatchObject({ cycle: 'monthly', options: { ip: '1' }, os: null })
    const kept = applyProductDefaults({ ...emptySpec(), cycle: 'quarterly', options: { ip: '2' }, os: 'debian' }, product)
    expect(kept).toMatchObject({ cycle: 'quarterly', options: { ip: '2' }, os: 'debian' })
  })
})

describe('defaultAccount', () => {
  const account = (id: string, status: Account['status']) => ({ id, status }) as Account
  it('prefers the first active account', () => {
    expect(defaultAccount([account('a', 'expired'), account('b', 'active')])?.id).toBe('b')
    expect(defaultAccount([account('a', 'unknown')])?.id).toBe('a')
    expect(defaultAccount([])).toBeUndefined()
  })
})
