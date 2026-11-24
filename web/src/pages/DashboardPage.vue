<script setup lang="ts">
// 概览页：统计卡片、最近事件（实时追加）、站点轮询状态与立即轮询
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { NBadge, NButton, NCard, NEllipsis, NGi, NGrid, NSpin, NStatistic, NTag, NText, useMessage, useThemeVars } from 'naive-ui'
import { eventsApi, sitesApi, statsApi } from '@/api'
import type { AppEvent, Site, Stats } from '@/api/types'
import { useAction, useAsync, useBusy } from '@/composables/useAsync'
import { onLive } from '@/composables/useLive'
import { useSites } from '@/composables/useSites'
import { hasTime } from '@/utils/format'
import { upsertById } from '@/utils/list'
import EventFeed from '@/components/EventFeed.vue'
import PageHeader from '@/components/PageHeader.vue'
import TimeAgo from '@/components/TimeAgo.vue'

const STATS_REFRESH_MS = 30000
const STATS_DEBOUNCE_MS = 1000
const MAX_EVENTS = 100

const message = useMessage()
const themeVars = useThemeVars()
const act = useAction()
const { isBusy, track } = useBusy()
const { sites, siteNames, loading: loadingSites } = useSites()
const stats = ref<Stats | null>(null)
const events = ref<AppEvent[]>([])

interface StatCard {
  key: string
  label: string
  value: string | number
  warn?: boolean
  to?: string
}
const statCards = computed<StatCard[]>(() => {
  const value = stats.value
  const pending = value?.pending_challenges ?? 0
  return [
    { key: 'sites', label: '站点', value: value?.sites ?? '-', to: '/sites' },
    { key: 'products', label: '商品', value: value?.products ?? '-', to: '/products' },
    { key: 'in_stock', label: '有货', value: value?.in_stock ?? '-', to: '/products' },
    { key: 'watches', label: '关注 / 自动锁单', value: value ? `${value.watches} / ${value.auto_watches}` : '-', to: '/watches' },
    { key: 'orders_24h', label: '24h 锁单', value: value?.orders_24h ?? '-', to: '/orders' },
    { key: 'pending', label: '待人工验证', value: value ? pending : '-', warn: pending > 0, to: '/challenges' },
    { key: 'events_24h', label: '24h 事件', value: value?.events_24h ?? '-' },
  ]
})

async function loadStats(): Promise<void> {
  try {
    stats.value = await statsApi.get()
  } catch {
    // 统计失败不打扰用户，下次定时刷新重试
  }
}

const { loading: loadingEvents, run: loadEvents } = useAsync(async () => {
  events.value = await eventsApi.list(50)
  return events.value
})

async function pollSite(site: Site): Promise<void> {
  const result = await track(site.id, () => act(() => sitesApi.poll(site.id)))
  if (!result) return
  message.success(`已抓取 ${result.products} 个商品`)
  void loadStats()
}

let statsTimer: ReturnType<typeof setInterval> | null = null
let debounceTimer: ReturnType<typeof setTimeout> | null = null
function scheduleStats(): void {
  if (debounceTimer) clearTimeout(debounceTimer)
  debounceTimer = setTimeout(() => {
    debounceTimer = null
    void loadStats()
  }, STATS_DEBOUNCE_MS)
}

onLive('event', (event) => {
  events.value = upsertById(events.value, event, { prepend: true }).slice(0, MAX_EVENTS)
  scheduleStats()
})
onLive('challenge', () => scheduleStats())
onLive('challenge_done', () => scheduleStats())
onLive('order', () => scheduleStats())

onMounted(() => {
  void loadStats()
  void loadEvents()
  statsTimer = setInterval(() => void loadStats(), STATS_REFRESH_MS)
})
onBeforeUnmount(() => {
  if (statsTimer) clearInterval(statsTimer)
  if (debounceTimer) clearTimeout(debounceTimer)
})
</script>

<template>
  <div class="page">
    <page-header title="概览" subtitle="补货监控与自动锁单运行状态" />

    <n-grid responsive="screen" cols="2 s:4 l:7" :x-gap="12" :y-gap="12" class="stats">
      <n-gi v-for="card in statCards" :key="card.key">
        <component :is="card.to ? RouterLink : 'div'" :to="card.to" class="stat-link">
          <n-card size="small" hoverable :style="card.warn ? { borderColor: themeVars.warningColor } : undefined">
            <n-statistic :label="card.label">
              <span :style="card.warn ? { color: themeVars.warningColor, fontWeight: 600 } : undefined">{{ card.value }}</span>
            </n-statistic>
          </n-card>
        </component>
      </n-gi>
    </n-grid>

    <n-grid responsive="screen" cols="1 m:2" :x-gap="16" :y-gap="16">
      <n-gi>
        <n-card title="最近事件" size="small">
          <template #header-extra>
            <router-link to="/orders"><n-text depth="3" style="font-size: 12px">订单记录</n-text></router-link>
          </template>
          <n-spin :show="loadingEvents && events.length === 0">
            <div class="scroll">
              <event-feed :events="events" :site-names="siteNames" />
            </div>
          </n-spin>
        </n-card>
      </n-gi>
      <n-gi>
        <n-card title="站点状态" size="small">
          <template #header-extra>
            <router-link to="/sites"><n-text depth="3" style="font-size: 12px">管理站点</n-text></router-link>
          </template>
          <n-spin :show="loadingSites && sites.length === 0">
            <div v-if="sites.length === 0 && !loadingSites" class="empty">
              <n-text depth="3">还没有站点，</n-text>
              <router-link to="/sites">去添加</router-link>
            </div>
            <div v-else class="scroll">
              <div v-for="site in sites" :key="site.id" class="site-row">
                <div class="site-main">
                  <div class="site-title">
                    <n-text strong>{{ site.name }}</n-text>
                    <n-tag size="tiny" :bordered="false">{{ site.kind }}</n-tag>
                    <n-tag v-if="!site.enabled" size="tiny" type="default" :bordered="false">已停用</n-tag>
                    <n-badge v-if="site.consecutive_fail > 0" :value="`连续失败 ${site.consecutive_fail}`" type="error" />
                  </div>
                  <n-text depth="3" class="site-meta">
                    有货 {{ site.in_stock_count }} / {{ site.product_count }} · 上次 <time-ago :value="site.last_poll_at" />
                    <template v-if="site.enabled && hasTime(site.next_poll_at)"> · 下次 <time-ago :value="site.next_poll_at" /></template>
                  </n-text>
                  <n-ellipsis v-if="site.last_error" :style="{ color: themeVars.errorColor, fontSize: '12px', maxWidth: '100%' }">
                    {{ site.last_error }}
                  </n-ellipsis>
                </div>
                <n-button size="small" secondary :loading="isBusy(site.id)" @click="pollSite(site)">立即轮询</n-button>
              </div>
            </div>
          </n-spin>
        </n-card>
      </n-gi>
    </n-grid>
  </div>
</template>

<style scoped>
.stats {
  margin-bottom: 16px;
}
.stat-link {
  display: block;
  color: inherit;
  text-decoration: none;
  height: 100%;
}
.stat-link :deep(.n-card) {
  height: 100%;
}
.scroll {
  max-height: 560px;
  overflow-y: auto;
}
.empty {
  padding: 24px 0;
  text-align: center;
}
.site-row {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 0;
  border-bottom: 1px solid v-bind('themeVars.dividerColor');
}
.site-row:last-child {
  border-bottom: none;
}
.site-main {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.site-title {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
}
.site-meta {
  font-size: 12px;
}
</style>
