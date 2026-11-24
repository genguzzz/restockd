// 下单规格（关注规则与商品直接下单共用）：表单状态、按商品补默认值、价格估算
import type { Account, BillingCycle, Money, Price, Product } from '@/api/types'

export const cycleMonths: Record<BillingCycle, number> = {
  monthly: 1,
  quarterly: 3,
  semiannually: 6,
  annually: 12,
  biennially: 24,
  triennially: 36,
  onetime: 1,
}

export interface OrderSpecState {
  cycle: BillingCycle | null
  options: Record<string, string>
  os: string | null
  hostname: string
  coupon: string
  gateway: string
  qty: number
  accountId: string | null
}

export function emptySpec(): OrderSpecState {
  return { cycle: null, options: {}, os: null, hostname: '', coupon: '', gateway: '', qty: 1, accountId: null }
}

// 按商品补齐周期与选项：保留仍然有效的已选值，其余取第一项；无效的 OS 清空
export function applyProductDefaults(spec: OrderSpecState, product: Product): OrderSpecState {
  const cycle = product.prices.some((item) => item.cycle === spec.cycle) ? spec.cycle : (product.prices[0]?.cycle ?? null)
  const options: Record<string, string> = {}
  for (const option of product.options ?? []) {
    const current = spec.options[option.key]
    const value = option.choices.some((choice) => choice.value === current) ? current : option.choices[0]?.value
    if (value !== undefined) options[option.key] = value
  }
  const osValid = !spec.os || (product.os_templates ?? []).some((item) => item.value === spec.os)
  return { ...spec, cycle, options, os: osValid ? spec.os : null }
}

// 默认账号：第一个会话有效的，否则第一个
export function defaultAccount(accounts: readonly Account[]): Account | undefined {
  return accounts.find((item) => item.status === 'active') ?? accounts[0]
}

export interface PriceEstimate {
  price: Price
  qty: number
  base: Money
  extra: Money
  total: Money
}

// 周期价 × 数量 + 选项月差价 × 周期月数 × 数量 + 初装费 × 数量（不含优惠码）
export function estimatePrice(
  product: Product | null | undefined,
  cycle: BillingCycle | null | undefined,
  options: Record<string, string>,
  qty: number,
): PriceEstimate | null {
  const price = cycle ? product?.prices.find((item) => item.cycle === cycle) : undefined
  if (!product || !cycle || !price) return null
  const currency = price.amount.currency
  const count = qty > 0 ? qty : 1
  let deltaMonthly = 0
  for (const option of product.options ?? []) {
    const choice = option.choices.find((item) => item.value === options[option.key])
    if (choice) deltaMonthly += choice.delta_monthly.cents
  }
  const base = price.amount.cents * count
  const extra = deltaMonthly * cycleMonths[cycle] * count
  return {
    price,
    qty: count,
    base: { cents: base, currency },
    extra: { cents: extra, currency },
    total: { cents: base + extra + price.setup.cents * count, currency },
  }
}
