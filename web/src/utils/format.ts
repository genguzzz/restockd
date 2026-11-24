// 纯函数格式化工具：金额、周期、规格、间隔、时间
import type { BillingCycle, Money, Specs } from '@/api/types'

const currencySymbols: Record<string, string> = { CNY: '¥', USD: '$', CAD: 'C$', EUR: '€', GBP: '£', HKD: 'HK$', AUD: 'A$', JPY: '¥' }

export function formatMoney(money: Money | null | undefined): string {
  if (!money) return '-'
  const symbol = currencySymbols[money.currency] ?? ''
  const value = (money.cents / 100).toFixed(2)
  if (symbol) return `${money.cents < 0 ? '-' : ''}${symbol}${Math.abs(money.cents / 100).toFixed(2)}`
  return money.currency ? `${value} ${money.currency}` : value
}

// 元（用户输入）与分互转
export function yuanToCents(yuan: number | null | undefined): number {
  if (yuan === null || yuan === undefined || Number.isNaN(yuan)) return 0
  return Math.round(yuan * 100)
}

export function centsToYuan(cents: number | null | undefined): number | null {
  if (!cents) return null
  return cents / 100
}

export const cycleLabels: Record<BillingCycle, string> = {
  monthly: '月付',
  quarterly: '季付',
  semiannually: '半年付',
  annually: '年付',
  biennially: '两年付',
  triennially: '三年付',
  onetime: '一次性',
}

export function cycleLabel(cycle: string | null | undefined): string {
  if (!cycle) return '-'
  return cycleLabels[cycle as BillingCycle] ?? cycle
}

export function formatSize(mb: number): string {
  if (mb >= 1024) {
    const gb = mb / 1024
    return `${Number.isInteger(gb) ? gb : gb.toFixed(1)} GB`
  }
  return `${mb} MB`
}

export function formatTraffic(gb: number): string {
  if (gb >= 1000) {
    const tb = gb / 1000
    return `${Number.isInteger(tb) ? tb : tb.toFixed(1)} TB`
  }
  return `${gb} GB`
}

// 规格摘要，如 "2 核 · 2 GB · 20 GB · 200 Mbps · 1 TB"
export function specsSummary(specs: Specs | null | undefined): string {
  if (!specs) return ''
  const parts: string[] = []
  if (specs.cpu) parts.push(`${specs.cpu} 核`)
  if (specs.ram_mb) parts.push(formatSize(specs.ram_mb))
  if (specs.disk_gb) parts.push(`${specs.disk_gb} GB 硬盘`)
  if (specs.bandwidth_mbps) parts.push(`${specs.bandwidth_mbps} Mbps`)
  if (specs.traffic_gb) parts.push(`${formatTraffic(specs.traffic_gb)} 流量`)
  return parts.join(' · ')
}

// 相对时间：过去 "3 分钟前"，未来 "2 分钟后"
export function timeAgo(value: string | null | undefined, now: number = Date.now()): string {
  if (!value || isZeroTime(value)) return '-'
  const time = new Date(value).getTime()
  if (Number.isNaN(time) || time <= 0) return '-'
  const diff = Math.round((now - time) / 1000)
  const future = diff < 0
  const seconds = Math.abs(diff)
  let text: string
  if (seconds < 5) return '刚刚'
  if (seconds < 60) text = `${seconds} 秒`
  else if (seconds < 3600) text = `${Math.floor(seconds / 60)} 分钟`
  else if (seconds < 86400) text = `${Math.floor(seconds / 3600)} 小时`
  else text = `${Math.floor(seconds / 86400)} 天`
  return future ? `${text}后` : `${text}前`
}

export function formatDateTime(value: string | null | undefined): string {
  if (!value || isZeroTime(value)) return '-'
  const date = new Date(value)
  if (Number.isNaN(date.getTime()) || date.getTime() <= 0) return '-'
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`
}

// Go 的 time.Time 总会序列化，零值 "0001-01-01T00:00:00Z" 表示「没有」
export function isZeroTime(value: string | null | undefined): boolean {
  return !value || value.startsWith('0001-01-01')
}

export function hasTime(value: string | null | undefined): value is string {
  return !isZeroTime(value)
}

// 轮询间隔：整小时 / 整分钟优先，如 "1 小时"、"3 分钟"、"90 秒"
export function formatInterval(sec: number): string {
  if (sec >= 3600 && sec % 3600 === 0) return `${sec / 3600} 小时`
  if (sec >= 60 && sec % 60 === 0) return `${sec / 60} 分钟`
  return `${sec} 秒`
}

// 时间是否在最近 ms 毫秒内（零值 / 无效时间为否）
export function isRecent(value: string | null | undefined, ms: number, now: number = Date.now()): boolean {
  if (!hasTime(value)) return false
  const time = new Date(value).getTime()
  return !Number.isNaN(time) && now - time < ms
}
