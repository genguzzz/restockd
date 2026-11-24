// format.ts 纯函数单元测试
import { describe, expect, it } from 'vitest'
import { centsToYuan, cycleLabel, formatMoney, isZeroTime, specsSummary, timeAgo, yuanToCents } from './format'

describe('formatMoney', () => {
  it('formats known currencies with symbols', () => {
    expect(formatMoney({ cents: 18800, currency: 'CNY' })).toBe('¥188.00')
    expect(formatMoney({ cents: 500, currency: 'USD' })).toBe('$5.00')
    expect(formatMoney({ cents: 1999, currency: 'CAD' })).toBe('C$19.99')
  })
  it('appends unknown currency code', () => {
    expect(formatMoney({ cents: 1234, currency: 'XYZ' })).toBe('12.34 XYZ')
  })
  it('shows bare number when currency is empty', () => {
    expect(formatMoney({ cents: 1234, currency: '' })).toBe('12.34')
  })
  it('puts the minus sign before the symbol', () => {
    expect(formatMoney({ cents: -1500, currency: 'CNY' })).toBe('-¥15.00')
    expect(formatMoney({ cents: -1234, currency: 'XYZ' })).toBe('-12.34 XYZ')
  })
  it('returns dash for null / undefined', () => {
    expect(formatMoney(null)).toBe('-')
    expect(formatMoney(undefined)).toBe('-')
  })
})

describe('cycleLabel', () => {
  it('maps known cycles', () => {
    expect(cycleLabel('monthly')).toBe('月付')
    expect(cycleLabel('annually')).toBe('年付')
    expect(cycleLabel('onetime')).toBe('一次性')
  })
  it('passes through unknown cycles', () => {
    expect(cycleLabel('weekly')).toBe('weekly')
  })
  it('returns dash for empty', () => {
    expect(cycleLabel('')).toBe('-')
    expect(cycleLabel(null)).toBe('-')
    expect(cycleLabel(undefined)).toBe('-')
  })
})

describe('specsSummary', () => {
  it('joins all fields', () => {
    expect(specsSummary({ cpu: '2', ram_mb: 2048, disk_gb: 20, bandwidth_mbps: 200, traffic_gb: 1000 })).toBe(
      '2 核 · 2 GB · 20 GB 硬盘 · 200 Mbps · 1 TB 流量',
    )
  })
  it('skips missing / zero fields', () => {
    expect(specsSummary({ cpu: '1', ram_mb: 768, traffic_gb: 0 })).toBe('1 核 · 768 MB')
    expect(specsSummary({ bandwidth_mbps: 100 })).toBe('100 Mbps')
  })
  it('formats non-integer units with one decimal', () => {
    expect(specsSummary({ ram_mb: 1536, traffic_gb: 1500 })).toBe('1.5 GB · 1.5 TB 流量')
  })
  it('returns empty string for empty input', () => {
    expect(specsSummary({})).toBe('')
    expect(specsSummary(null)).toBe('')
  })
})

describe('timeAgo', () => {
  const now = Date.parse('2026-11-24T08:00:00Z')
  const at = (offsetSeconds: number) => new Date(now - offsetSeconds * 1000).toISOString()
  it('describes past times', () => {
    expect(timeAgo(at(30), now)).toBe('30 秒前')
    expect(timeAgo(at(180), now)).toBe('3 分钟前')
    expect(timeAgo(at(2 * 3600 + 10), now)).toBe('2 小时前')
    expect(timeAgo(at(3 * 86400), now)).toBe('3 天前')
  })
  it('describes future times with 后', () => {
    expect(timeAgo(at(-120), now)).toBe('2 分钟后')
    expect(timeAgo(at(-30), now)).toBe('30 秒后')
  })
  it('returns 刚刚 within 5 seconds either way', () => {
    expect(timeAgo(at(0), now)).toBe('刚刚')
    expect(timeAgo(at(4), now)).toBe('刚刚')
    expect(timeAgo(at(-4), now)).toBe('刚刚')
  })
  it('returns dash for invalid / empty / zero time', () => {
    expect(timeAgo('not a date', now)).toBe('-')
    expect(timeAgo('', now)).toBe('-')
    expect(timeAgo(null, now)).toBe('-')
    expect(timeAgo('0001-01-01T00:00:00Z', now)).toBe('-')
  })
})

describe('yuanToCents / centsToYuan', () => {
  it('rounds to whole cents', () => {
    expect(yuanToCents(188)).toBe(18800)
    expect(yuanToCents(19.99)).toBe(1999)
    expect(yuanToCents(0.1 + 0.2)).toBe(30)
    expect(yuanToCents(1.005)).toBe(100)
  })
  it('treats empty input as 0', () => {
    expect(yuanToCents(null)).toBe(0)
    expect(yuanToCents(undefined)).toBe(0)
    expect(yuanToCents(Number.NaN)).toBe(0)
  })
  it('converts cents back, 0 meaning unset', () => {
    expect(centsToYuan(18800)).toBe(188)
    expect(centsToYuan(1999)).toBe(19.99)
    expect(centsToYuan(0)).toBeNull()
    expect(centsToYuan(null)).toBeNull()
  })
})

describe('isZeroTime', () => {
  it('detects Go zero time and empty', () => {
    expect(isZeroTime('0001-01-01T00:00:00Z')).toBe(true)
    expect(isZeroTime('')).toBe(true)
    expect(isZeroTime(undefined)).toBe(true)
    expect(isZeroTime('2026-11-24T07:40:00Z')).toBe(false)
  })
})
