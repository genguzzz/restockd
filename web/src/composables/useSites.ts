// 全局共享的站点 / Provider 列表：首次使用时加载一次，站点由 SSE site 推送与本地增删保持最新
import { computed, ref, shallowRef, watch } from 'vue'
import type { SelectOption } from 'naive-ui'
import { providersApi, sitesApi } from '@/api'
import type { ProviderMeta, Site, SiteInput } from '@/api/types'
import { removeById, upsertById } from '@/utils/list'
import { useAction } from './useAsync'
import { liveConnected, subscribe } from './useLive'

// 只加载一次的共享列表；失败后下次使用时重试
function sharedList<T>(load: () => Promise<T[]>) {
  const items = shallowRef<T[]>([])
  const loading = ref(false)
  let loaded = false
  let inflight: Promise<T[]> | null = null
  const reload = (): Promise<T[]> => {
    inflight ??= load()
      .then((list) => {
        items.value = list
        loaded = true
        return list
      })
      .finally(() => {
        loading.value = false
        inflight = null
      })
    loading.value = true
    return inflight
  }
  const ensure = (act: ReturnType<typeof useAction>) => {
    if (!loaded && !inflight) void act(reload)
  }
  // 已加载过才刷新（静默，失败等下次）
  const refresh = () => {
    if (loaded) reload().catch(() => undefined)
  }
  return { items, loading, reload, ensure, refresh }
}

const siteList = sharedList(() => sitesApi.list())
const providerList = sharedList(() => providersApi.list())
subscribe('site', (site) => upsertSite(site))
// 断线重连（非首次连接）后重新拉取，补上断线期间错过的推送
let everConnected = false
watch(liveConnected, (connected) => {
  if (!connected) return
  if (everConnected) siteList.refresh()
  everConnected = true
})

const siteNames = computed<Record<string, string>>(() => Object.fromEntries(siteList.items.value.map((site) => [site.id, site.name])))
const siteOptions = computed<SelectOption[]>(() => siteList.items.value.map((site) => ({ label: site.name, value: site.id })))

export function upsertSite(site: Site): void {
  siteList.items.value = upsertById(siteList.items.value, site)
}

export function removeSite(id: string): void {
  siteList.items.value = removeById(siteList.items.value, id)
}

// 只取可写字段，避免把只读状态回传
export function toSiteInput(site: Site): SiteInput {
  return {
    kind: site.kind,
    name: site.name,
    base_url: site.base_url,
    config: { ...site.config },
    interval_sec: site.interval_sec,
    fetch_mode: site.fetch_mode,
    egress: [...(site.egress ?? [])],
    monitor_account_id: site.monitor_account_id ?? '',
    enabled: site.enabled,
  }
}

export function useSites() {
  const act = useAction()
  siteList.ensure(act)
  return {
    sites: siteList.items,
    loading: siteList.loading,
    siteNames,
    siteOptions,
    reload: () => act(siteList.reload),
  }
}

export function useProviders() {
  providerList.ensure(useAction())
  const providers = providerList.items
  const providerOf = (kind: string): ProviderMeta | undefined => providers.value.find((item) => item.kind === kind)
  return { providers, providerOf }
}
