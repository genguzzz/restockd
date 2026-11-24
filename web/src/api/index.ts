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
  ProviderMeta,
  Quote,
  Settings,
  Site,
  SiteInput,
  Stats,
  StockStatus,
  Watch,
  WatchInput,
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
}

export const productsApi = {
  list: (query: ProductQuery = {}) => http.get<Product[]>('/api/products', { ...query }),
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
  order: (id: string, dryRun: boolean) =>
    http.post<Order>(`/api/watches/${encodeURIComponent(id)}/order`, { dry_run: dryRun }),
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

export const settingsApi = {
  get: () => http.get<Settings>('/api/settings'),
  save: (settings: Settings) => http.put<Settings>('/api/settings', settings),
  testNotify: () => http.post<{ ok: boolean; results: Record<string, string> }>('/api/settings/test-notify'),
}

export const statsApi = {
  get: () => http.get<Stats>('/api/stats'),
}
