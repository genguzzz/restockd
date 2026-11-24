// 与 docs/api.md 一一对应的实体类型

export interface Money {
  cents: number
  currency: string
}

export type BillingCycle =
  | 'monthly'
  | 'quarterly'
  | 'semiannually'
  | 'annually'
  | 'biennially'
  | 'triennially'
  | 'onetime'

export type StockStatus = 'in_stock' | 'out_of_stock' | 'preorder' | 'closed' | 'unknown'
export type FetchMode = 'direct' | 'flaresolverr'
export type AccountStatus = 'unknown' | 'active' | 'expired' | 'rate_limited'
export type OrderStatus = 'pending' | 'placed' | 'dry_run' | 'failed'
export type EventType =
  | 'restock'
  | 'sold_out'
  | 'price_change'
  | 'new_product'
  | 'order'
  | 'challenge'
  | 'poll_error'
  | 'account'
export type ChallengeKind = 'cloudflare' | 'turnstile' | 'recaptcha_v2' | 'recaptcha_v3' | 'hcaptcha'
export type FieldType = 'text' | 'list' | 'bool' | 'password'

export interface ProviderField {
  key: string
  label: string
  type: FieldType
  required: boolean
  default?: string
  placeholder?: string
  help?: string
}

export interface ProviderMeta {
  kind: string
  name: string
  description: string
  capabilities: {
    catalog: boolean
    coupon: boolean
    order: boolean
    session_check: boolean
    login?: boolean
    stock_needs_login: boolean
  }
  default_fetch_mode: FetchMode
  min_interval_sec: number
  default_interval_sec: number
  captcha?: ChallengeKind[]
  config_fields: ProviderField[]
}

export interface Site {
  id: string
  kind: string
  name: string
  base_url: string
  config: Record<string, string>
  interval_sec: number
  fetch_mode: FetchMode
  /** 出口优先级（出口 ID，'direct' 为直连），空表示用全局默认；出错按顺序回退 */
  egress?: string[]
  monitor_account_id?: string
  enabled: boolean
  last_poll_at?: string
  last_error?: string
  next_poll_at?: string
  consecutive_fail: number
  product_count: number
  in_stock_count: number
}

export type SiteInput = Pick<
  Site,
  'kind' | 'name' | 'base_url' | 'config' | 'interval_sec' | 'fetch_mode' | 'egress' | 'monitor_account_id' | 'enabled'
> & { id?: string }

export interface Choice {
  value: string
  label: string
  delta_monthly: Money
}

export interface ConfigOption {
  key: string
  label: string
  choices: Choice[]
}

export interface Specs {
  cpu?: string
  ram_mb?: number
  disk_gb?: number
  bandwidth_mbps?: number
  traffic_gb?: number
  location?: string
  ip_type?: string
  extra?: Record<string, string>
}

export interface Price {
  cycle: BillingCycle
  amount: Money
  setup: Money
  ref_id?: string
}

export interface Stock {
  status: StockStatus
  qty?: number
  reason?: string
}

export interface Product {
  site_id: string
  id: string
  group?: string
  name: string
  url?: string
  specs: Specs
  prices: Price[]
  options?: ConfigOption[]
  os_templates?: Choice[]
  stock: Stock
  no_discount?: boolean
  fetched_at: string
  watched: boolean
}

export interface OrderDefaults {
  gateway?: string
  os?: string
  hostname?: string
  ssh_key?: string
}

export interface Account {
  id: string
  site_id: string
  label: string
  user_agent?: string
  status: AccountStatus
  identity?: string
  balance?: Money
  checked_at?: string
  last_error?: string
  defaults: OrderDefaults
  created_at: string
  cookie_names: string[]
  /** 托管登录用户名；密码永不回传，只有 has_password */
  username?: string
  has_password: boolean
  login_at?: string
  /** 站点拒绝了当前密码，自动登录已暂停 */
  credentials_rejected?: boolean
}

export interface AccountInput {
  id?: string
  site_id: string
  label: string
  user_agent?: string
  defaults: OrderDefaults
  cookies_raw?: string
  /** 省略表示不修改 */
  username?: string
  /** 省略或 "******" 不修改，空串清除 */
  password?: string
}

export interface Watch {
  id: string
  site_id: string
  product_id: string
  product_name?: string
  cycle: BillingCycle
  options?: Record<string, string>
  os?: string
  hostname?: string
  coupon?: string
  gateway?: string
  qty: number
  account_id?: string
  auto_order: boolean
  max_price_cents?: number
  max_orders: number
  orders_placed: number
  notify: boolean
  enabled: boolean
  created_at: string
  last_triggered?: string
}

export type WatchInput = Omit<Watch, 'id' | 'orders_placed' | 'created_at' | 'last_triggered' | 'product_name'> & {
  id?: string
}

export interface Quote {
  subtotal: Money
  discount: Money
  total: Money
  coupon_valid: boolean
  message?: string
}

export interface OrderSpec {
  product_id: string
  cycle: BillingCycle
  options?: Record<string, string>
  os?: string
  hostname?: string
  ssh_key?: string
  coupon?: string
  gateway?: string
  qty: number
  dry_run: boolean
}

export interface OrderResult {
  order_id?: string
  invoice_id?: string
  pay_url?: string
  total: Money
  message?: string
}

export interface Order {
  id: string
  watch_id?: string
  site_id: string
  account_id: string
  product_id: string
  product_name: string
  spec: OrderSpec
  status: OrderStatus
  result: OrderResult
  error?: string
  created_at: string
  duration_ms: number
}

export interface AppEvent {
  id: number
  type: EventType
  site_id: string
  product_id?: string
  title: string
  detail?: string
  created_at: string
}

export interface Challenge {
  kind: ChallengeKind
  site_key?: string
  page_url: string
  action?: string
  site_id: string
  account_id?: string
  purpose: string
}

export interface PendingChallenge {
  id: string
  challenge: Challenge
  created_at: string
  expires_at: string
}

export interface ChallengeSolveInput {
  token?: string
  cookies_raw?: string
  user_agent?: string
}

export interface Settings {
  telegram: { enabled: boolean; bot_token: string; chat_id: string; api_base: string }
  email: {
    enabled: boolean
    host: string
    port: number
    username: string
    password: string
    from: string
    to: string
    starttls: boolean
  }
  bark: { enabled: boolean; device_urls: string; group: string; sound: string; icon: string }
  solver: { flaresolverr_url: string; external_url: string; external_token: string; manual_timeout_sec: number }
  /** 无关注站点的降频：无人访问 idle、有人访问 viewer（秒，0 为默认 3600 / 300） */
  polling: { idle_interval_sec: number; viewer_interval_sec: number }
  /** 出口线路：登记的代理 + 未单独指定出口的站点使用的默认优先级 */
  egress: { proxies: EgressProxy[]; default: string[] }
  notify_events: EventType[]
}

export interface EgressProxy {
  id: string
  name: string
  /** http://user:pass@host:port 或 socks5://host:port；读取时密码为 ****** */
  url: string
  enabled: boolean
}

export interface EgressTestResult {
  ok: boolean
  ip?: string
  loc?: string
  colo?: string
  ms?: number
  error?: string
}

export interface Stats {
  sites: number
  products: number
  in_stock: number
  watches: number
  auto_watches: number
  orders_24h: number
  pending_challenges: number
  events_24h: number
}

// SSE 事件名 → 数据类型
export interface StreamEvents {
  event: AppEvent
  site: Site
  challenge: PendingChallenge
  challenge_done: { id: string }
  order: Order
}
