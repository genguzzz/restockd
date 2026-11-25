// 补货历史的展示文案。时长按轮询观测，误差约一个轮询间隔。
import type { StockHistory, StockWindow } from '@/api/types'
import { timeAgo } from './format'

// 人读时长，两级精度：「6 秒」「3 分钟」「2 小时 5 分钟」「2 天 2 小时」（与后端通知一致）
export function formatSpan(ms: number): string {
  const seconds = Math.max(0, Math.round(ms / 1000))
  const minutes = Math.floor(seconds / 60)
  const hours = Math.floor(seconds / 3600)
  const days = Math.floor(seconds / 86400)
  const pair = (major: string, minor: number, unit: string) => (minor > 0 ? `${major} ${minor} ${unit}` : major)
  if (seconds < 60) return `${seconds} 秒`
  if (seconds < 3600) return `${minutes} 分钟`
  if (seconds < 86400) return pair(`${hours} 小时`, minutes % 60, '分钟')
  return pair(`${days} 天`, hours % 24, '小时')
}

/** 在售时长：已结束按 ended_at，在售中按 now */
export function windowDuration(window: StockWindow, now: number = Date.now()): number {
  const end = window.ended_at ? new Date(window.ended_at).getTime() : now
  return end - new Date(window.started_at).getTime()
}

/** 时长文案；开始于首次抓取的加「至少」 */
export function durationText(window: StockWindow, now: number = Date.now()): string {
  return (window.baseline ? '至少 ' : '') + formatSpan(windowDuration(window, now))
}

/** 商品卡片一行摘要：在售中「已在售 X」，否则「上次补货 X前 · 在售 Y」，附近 30 天补货次数 */
export function stockHistorySummary(history: StockHistory | undefined, now: number = Date.now()): string {
  if (!history) return ''
  const parts: string[] = []
  if (history.current) parts.push(`已在售 ${durationText(history.current, now)}`)
  else if (history.last) parts.push(`上次补货 ${timeAgo(history.last.started_at, now)} · 在售 ${durationText(history.last, now)}`)
  if (history.restocks_30d > 0) parts.push(`30 天补货 ${history.restocks_30d} 次`)
  return parts.join(' · ')
}

/** 多段在售期的统计（不含 Baseline 段）：次数与平均在售时长（毫秒，含在售中的段） */
export function windowStats(windows: StockWindow[], now: number = Date.now()): { count: number; averageMs: number } {
  const observed = windows.filter((window) => !window.baseline)
  if (observed.length === 0) return { count: 0, averageMs: 0 }
  const total = observed.reduce((sum, window) => sum + windowDuration(window, now), 0)
  return { count: observed.length, averageMs: total / observed.length }
}
