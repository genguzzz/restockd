import { describe, expect, it } from 'vitest'
import type { StockWindow } from '@/api/types'
import { durationText, formatSpan, stockHistorySummary, windowStats } from './stockHistory'

const now = Date.parse('2026-11-25T12:00:00Z')
const iso = (offsetMs: number) => new Date(now + offsetMs).toISOString()
const win = (start: number, end?: number, baseline = false): StockWindow => ({
  id: 1,
  site_id: 's',
  product_id: 'p',
  started_at: iso(start),
  last_seen_at: iso(end ?? 0),
  ended_at: end === undefined ? undefined : iso(end),
  baseline,
})
const MIN = 60_000
const HOUR = 60 * MIN

describe('formatSpan', () => {
  it('uses two units of precision', () => {
    expect(formatSpan(6_000)).toBe('6 秒')
    expect(formatSpan(3 * MIN + 20_000)).toBe('3 分钟')
    expect(formatSpan(2 * HOUR)).toBe('2 小时')
    expect(formatSpan(2 * HOUR + 5 * MIN)).toBe('2 小时 5 分钟')
    expect(formatSpan(50 * HOUR)).toBe('2 天 2 小时')
  })
})

describe('stockHistorySummary', () => {
  it('shows the running window', () => {
    expect(stockHistorySummary({ current: win(-5 * MIN), restocks_30d: 2 }, now)).toBe('已在售 5 分钟 · 30 天补货 2 次')
  })
  it('shows the last window when sold out', () => {
    expect(stockHistorySummary({ last: win(-3 * HOUR, -3 * HOUR + 6_000), restocks_30d: 1 }, now)).toBe('上次补货 3 小时前 · 在售 6 秒 · 30 天补货 1 次')
  })
  it('marks baseline windows as lower bounds', () => {
    expect(durationText(win(-MIN, 0, true), now)).toBe('至少 1 分钟')
    expect(stockHistorySummary(undefined, now)).toBe('')
  })
})

describe('windowStats', () => {
  it('averages observed windows only', () => {
    expect(windowStats([win(-10 * MIN, -8 * MIN), win(-6 * MIN, -2 * MIN), win(-HOUR, 0, true)], now)).toEqual({ count: 2, averageMs: 3 * MIN })
  })
})
