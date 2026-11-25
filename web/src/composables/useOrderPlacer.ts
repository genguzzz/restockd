// 真实锁单的防重复：下单标记有效期内已锁过单先确认再带 force 下单；后端返回 409（重复锁单）时展示已有订单并可强制重试
import { h } from 'vue'
import { useDialog, useMessage } from 'naive-ui'
import { ApiError } from '@/api/client'
import type { Order, Product } from '@/api/types'
import { formatDateTime, hasTime, isRecent } from '@/utils/format'
import { errorText } from './useAsync'

export const RECENT_ORDER_MS = 24 * 3600 * 1000

// 之前的那次锁单（来自 Product.last_order / last_placed_at 或 409 响应里的订单）
export interface PriorOrder {
  at: string
  payUrl?: string
  invoiceId?: string
}

type OrderLike = { created_at?: unknown; pay_url?: string; invoice_id?: string; result?: { pay_url?: string; invoice_id?: string } }

// 409 且响应体带订单（{order: …} 或订单本身）时视为重复锁单；其他 409（如订单进行中）返回 null
export function duplicateOrderOf(err: unknown): PriorOrder | null {
  if (!(err instanceof ApiError) || err.status !== 409 || !err.data || typeof err.data !== 'object') return null
  const data = err.data as { order?: OrderLike } & OrderLike
  const order = data.order ?? data
  if (typeof order.created_at !== 'string') return null
  return {
    at: order.created_at,
    payUrl: order.pay_url ?? order.result?.pay_url,
    invoiceId: order.invoice_id ?? order.result?.invoice_id,
  }
}

function priorLine(prior: PriorOrder) {
  const bill = prior.payUrl
    ? ['，', h('a', { href: prior.payUrl, target: '_blank', rel: 'noopener noreferrer' }, prior.invoiceId ? `账单 ${prior.invoiceId}` : '账单链接')]
    : prior.invoiceId
      ? [`，账单 ${prior.invoiceId}`]
      : []
  return h('div', ['近期已对该商品锁单（', formatDateTime(prior.at), ...bill, '），确定再下一单？'])
}

export function useOrderPlacer() {
  const dialog = useDialog()
  const message = useMessage()

  const ask = (title: string, lines: (string | ReturnType<typeof h>)[], positiveText: string) =>
    new Promise<boolean>((resolve) => {
      dialog.warning({
        title,
        content: () => h('div', { style: 'display: flex; flex-direction: column; gap: 6px' }, lines.map((line) => (typeof line === 'string' ? h('div', line) : line))),
        positiveText,
        negativeText: '取消',
        onPositiveClick: () => resolve(true),
        onAfterLeave: () => resolve(false),
      })
    })

  // run(force) 发起下单；confirm 为真实锁单前的确认文案（可选），prior 为已知的上一次锁单
  return async function place(
    run: (force: boolean) => Promise<Order>,
    options: { dryRun: boolean; confirm?: string; prior?: PriorOrder | null },
  ): Promise<Order | null> {
    let force = false
    if (!options.dryRun) {
      const recent = options.prior && isRecent(options.prior.at, RECENT_ORDER_MS) ? options.prior : null
      const lines = [...(options.confirm ? [options.confirm] : []), ...(recent ? [priorLine(recent)] : [])]
      if (lines.length > 0 && !(await ask(recent ? '重复锁单确认' : '确认锁单', lines, recent ? '再下一单' : '锁单'))) return null
      force = recent !== null
    }
    try {
      return await run(force)
    } catch (err) {
      const duplicate = force ? null : duplicateOrderOf(err)
      if (!duplicate) {
        message.error(errorText(err))
        return null
      }
      if (!(await ask('重复锁单', [priorLine(duplicate)], '再下一单'))) return null
      try {
        return await run(true)
      } catch (retryErr) {
        message.error(errorText(retryErr))
        return null
      }
    }
  }
}

// 商品上记录的最近一次真实锁单
export function priorOfProduct(product: Product): PriorOrder | null {
  if (!hasTime(product.last_placed_at)) return null
  const placed = product.last_order?.status === 'placed' ? product.last_order : null
  return { at: product.last_placed_at, payUrl: placed?.pay_url, invoiceId: placed?.invoice_id }
}
