// 状态 / 类型 → 标签文案与颜色，所有页面共用一份
import type { AccountStatus, ChallengeKind, EventType, FetchMode, OrderStatus, StockStatus } from '@/api/types'

export type TagType = 'default' | 'success' | 'info' | 'warning' | 'error'

export interface TagMeta {
  label: string
  type: TagType
}

// 未知取值（后端新增枚举等）原样显示为默认色
export function metaOf(map: Readonly<Record<string, TagMeta>>, key: string): TagMeta {
  return map[key] ?? { label: key, type: 'default' }
}

export const accountStatusMeta: Record<AccountStatus, TagMeta> = {
  active: { label: '有效', type: 'success' },
  expired: { label: '已失效', type: 'error' },
  unknown: { label: '未校验', type: 'default' },
}

export const orderStatusMeta: Record<OrderStatus, TagMeta> = {
  placed: { label: '已锁单', type: 'success' },
  dry_run: { label: '试下单', type: 'info' },
  pending: { label: '进行中', type: 'warning' },
  failed: { label: '失败', type: 'error' },
  cancelled: { label: '已取消', type: 'default' },
}

export const stockMeta: Record<StockStatus, TagMeta> = {
  in_stock: { label: '有货', type: 'success' },
  out_of_stock: { label: '缺货', type: 'error' },
  preorder: { label: '预售', type: 'info' },
  closed: { label: '关闭', type: 'warning' },
  unknown: { label: '未知', type: 'default' },
}

export const eventMeta: Record<EventType, TagMeta> = {
  restock: { label: '补货', type: 'success' },
  sold_out: { label: '售罄', type: 'error' },
  price_change: { label: '价格变动', type: 'warning' },
  new_product: { label: '新品', type: 'success' },
  order: { label: '锁单', type: 'info' },
  challenge: { label: '人工验证', type: 'warning' },
  poll_error: { label: '轮询失败', type: 'error' },
  account: { label: '账号异常', type: 'error' },
}

export const fetchModeMeta: Record<FetchMode, TagMeta> = {
  direct: { label: '直连', type: 'default' },
  flaresolverr: { label: 'FlareSolverr', type: 'info' },
}

export const challengeKindMeta: Record<ChallengeKind, TagMeta> = {
  turnstile: { label: 'Turnstile', type: 'warning' },
  recaptcha_v2: { label: 'reCAPTCHA v2', type: 'warning' },
  recaptcha_v3: { label: 'reCAPTCHA v3', type: 'warning' },
  hcaptcha: { label: 'hCaptcha', type: 'warning' },
}

const notifyChannelLabels: Record<string, string> = { telegram: 'Telegram', email: '邮件', bark: 'Bark' }

// 测试通知单个渠道的结果："ok" / "disabled" / "not_configured" / 错误信息
export function notifyResultMeta(channel: string, result: string): TagMeta {
  const name = notifyChannelLabels[channel] ?? channel
  if (result === 'ok') return { label: `${name}：成功`, type: 'success' }
  if (result === 'disabled' || result === '') return { label: `${name}：未启用`, type: 'default' }
  // 已启用但配置不完整（旧版后端为 "not configured"）
  if (result === 'not_configured' || result === 'not configured') return { label: `${name}：未配置完整`, type: 'warning' }
  return { label: `${name}：${result}`, type: 'error' }
}
