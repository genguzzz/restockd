// 按资源分组的接口函数，页面只通过这里访问后端
import { http } from './client'
import type {
  Account,
  AccountInput,
  AppEvent,
  ChallengeSolveInput,
  Order,
  PendingChallenge,
  Product,
  ProductOrderInput,
  ProviderMeta,
  Quote,
  Settings,
  Site,
  SiteInput,
  Stats,
  StockStatus,
  Watch,
  WatchInput,
  EgressTestResult,
} from './types'

type Ok = { ok: boolean }

export const authApi = {
  login: (password: string) => http.post<Ok>('/api/login', { password }),
  logout: () => http.post<Ok>('/api/logout'),
  me: () => http.get<{ ok: boolean; version: string }>('/api/me'),
}

export const providersApi = {
  list: () => http.get<ProviderMeta[]>('/api/providers'),
}

export const sitesApi = {
  list: () => http.get<Site[]>('/api/sites'),
  create: (input: SiteInput) => http.post<Site>('/api/sites', input),
  update: (id: string, input: SiteInput) => http.put<Site>(`/api/sites/${encodeURIComponent(id)}`, input),
  remove: (id: string) => http.del<Ok>(`/api/sites/${encodeURIComponent(id)}`),
  poll: (id: string) => http.post<{ ok: boolean; products: number }>(`/api/sites/${encodeURIComponent(id)}/poll`),
}

export interface ProductQuery {
  site_id?: string
  status?: StockStatus | ''
  q?: string
  /** 1：连同已屏蔽的商品一起返回（带 hidden: true） */
  include_hidden?: 1
}

const productPath = (siteId: string, productId: string) =>
  `/api/sites/${encodeURIComponent(siteId)}/products/${encodeURIComponent(productId)}`

export const productsApi = {
  list: (query: ProductQuery = {}) => http.get<Product[]>('/api/products', { ...query }),
  hide: (siteId: string, productId: string) => http.post<Site>(`${productPath(siteId, productId)}/hide`),
  unhide: (siteId: string, productId: string) => http.post<Site>(`${productPath(siteId, productId)}/unhide`),
  order: (siteId: string, productId: string, input: ProductOrderInput) =>
    http.post<Order>(`${productPath(siteId, productId)}/order`, input),
}

export const accountsApi = {
  list: (siteId?: string) => http.get<Account[]>('/api/accounts', { site_id: siteId }),
  create: (input: AccountInput) => http.post<Account>('/api/accounts', input),
  update: (id: string, input: AccountInput) => http.put<Account>(`/api/accounts/${encodeURIComponent(id)}`, input),
  remove: (id: string) => http.del<Ok>(`/api/accounts/${encodeURIComponent(id)}`),
  setCookies: (id: string, raw: string, userAgent?: string) =>
    http.post<{ ok: boolean; cookie_names: string[] }>(`/api/accounts/${encodeURIComponent(id)}/cookies`, {
      raw,
      user_agent: userAgent ?? '',
    }),
  check: (id: string) => http.post<Account>(`/api/accounts/${encodeURIComponent(id)}/check`),
  login: (id: string) => http.post<Account>(`/api/accounts/${encodeURIComponent(id)}/login`),
}

export const watchesApi = {
  list: (siteId?: string) => http.get<Watch[]>('/api/watches', { site_id: siteId }),
  create: (input: WatchInput) => http.post<Watch>('/api/watches', input),
  update: (id: string, input: WatchInput) => http.put<Watch>(`/api/watches/${encodeURIComponent(id)}`, input),
  remove: (id: string) => http.del<Ok>(`/api/watches/${encodeURIComponent(id)}`),
  quote: (id: string) => http.post<Quote>(`/api/watches/${encodeURIComponent(id)}/quote`),
  order: (id: string, dryRun: boolean, force = false) =>
    http.post<Order>(`/api/watches/${encodeURIComponent(id)}/order`, force ? { dry_run: dryRun, force } : { dry_run: dryRun }),
}

export const ordersApi = {
  list: (limit = 100) => http.get<Order[]>('/api/orders', { limit }),
}

export const eventsApi = {
  list: (limit = 200, siteId?: string) => http.get<AppEvent[]>('/api/events', { limit, site_id: siteId }),
}

export const challengesApi = {
  list: () => http.get<PendingChallenge[]>('/api/challenges'),
  solve: (id: string, input: ChallengeSolveInput) =>
    http.post<Ok>(`/api/challenges/${encodeURIComponent(id)}/solve`, input),
  cancel: (id: string) => http.post<Ok>(`/api/challenges/${encodeURIComponent(id)}/cancel`),
}

export const egressApi = {
  test: (id: string) => http.post<EgressTestResult>(`/api/egress/${encodeURIComponent(id)}/test`),
}

// 密钥字段（设置里的 token / 密码、账号密码）读取时的掩码；原样传回表示不修改，空串表示清除
export const SECRET_MASK = '******'

// Go 的 nil 切片序列化为 null：在这里统一补成空数组，页面拿到的 Settings 总是完整的
export function normalizeSettings(raw: Settings): Settings {
  const egress = raw.egress ?? { proxies: [], default: [] }
  return {
    ...raw,
    egress: { proxies: egress.proxies ?? [], default: egress.default ?? [] },
    notify_events: raw.notify_events ?? [],
  }
}

export const settingsApi = {
  get: () => http.get<Settings>('/api/settings').then(normalizeSettings),
  save: (settings: Settings) => http.put<Settings>('/api/settings', settings).then(normalizeSettings),
  testNotify: () => http.post<{ ok: boolean; results: Record<string, string> }>('/api/settings/test-notify'),
}

export const statsApi = {
  get: () => http.get<Stats>('/api/stats'),
}
