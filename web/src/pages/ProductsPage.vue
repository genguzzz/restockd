<script setup lang="ts">
// 商品页：按站点 / 库存 / 关键词筛选，卡片或表格视图，一键关注（打开关注抽屉）
import { useIsMobile } from '@/composables/useIsMobile'
import { computed, h, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import {
  NButton,
  NCard,
  NDataTable,
  NEmpty,
  NInput,
  NRadioButton,
  NRadioGroup,
  NSelect,
  NSpace,
  NSpin,
  NTag,
  NText,
  useMessage,
  type DataTableColumns,
  type SelectOption,
} from 'naive-ui'
import { productsApi, sitesApi } from '@/api'
import type { EventType, Product, Site, StockStatus } from '@/api/types'
import { onLive } from '@/composables/useLive'
import { stockLabels } from '@/utils/format'
import CycleLabel from '@/components/CycleLabel.vue'
import MoneyText from '@/components/MoneyText.vue'
import PageHeader from '@/components/PageHeader.vue'
import SpecLine from '@/components/SpecLine.vue'
import StockBadge from '@/components/StockBadge.vue'
import TimeAgo from '@/components/TimeAgo.vue'
import WatchFormDrawer from '@/components/WatchFormDrawer.vue'

type ViewMode = 'cards' | 'table'

const KEYWORD_DEBOUNCE_MS = 300
const LIVE_DEBOUNCE_MS = 1000
const VIEW_STORAGE_KEY = 'restockd.products.view'
const refreshEventTypes: EventType[] = ['restock', 'sold_out', 'new_product', 'price_change']
const orderableStatuses: StockStatus[] = ['in_stock', 'preorder']

const readView = (): ViewMode => {
  try {
    return localStorage.getItem(VIEW_STORAGE_KEY) === 'table' ? 'table' : 'cards'
  } catch {
    return 'cards'
  }
}

const message = useMessage()
const view = ref<ViewMode>(readView())
const sites = ref<Site[]>([])
const products = ref<Product[]>([])
const loading = ref(false)
const loaded = ref(false)
const siteId = ref<string | null>('')
const status = ref<string | null>('')
const keyword = ref('')
const appliedKeyword = ref('')
const drawerShow = ref(false)
const drawerProduct = ref<Product | null>(null)

const siteNames = computed<Record<string, string>>(() => Object.fromEntries(sites.value.map((site) => [site.id, site.name])))
const siteOptions = computed<SelectOption[]>(() => [
  { label: '全部站点', value: '' },
  ...sites.value.map((site) => ({ label: site.name, value: site.id })),
])
const statusOptions: SelectOption[] = [
  { label: '全部状态', value: '' },
  ...(Object.keys(stockLabels) as StockStatus[]).map((key) => ({ label: stockLabels[key], value: key })),
]
const hasFilter = computed(() => Boolean(siteId.value || status.value || appliedKeyword.value))

// 可下单（有货 / 预售）优先，其次按站点、名称
const sortedProducts = computed<Product[]>(() => {
  const rank = (item: Product): number => (orderableStatuses.includes(item.stock.status) ? 0 : 1)
  return [...products.value].sort((a, b) => {
    const byRank = rank(a) - rank(b)
    if (byRank !== 0) return byRank
    const bySite = (siteNames.value[a.site_id] ?? a.site_id).localeCompare(siteNames.value[b.site_id] ?? b.site_id)
    if (bySite !== 0) return bySite
    return a.name.localeCompare(b.name)
  })
})

async function loadSites(): Promise<void> {
  try {
    sites.value = await sitesApi.list()
  } catch (err) {
    message.error(err instanceof Error ? err.message : String(err))
  }
}

let requestSeq = 0
async function loadProducts(): Promise<void> {
  const seq = ++requestSeq
  loading.value = true
  try {
    const list = await productsApi.list({
      site_id: siteId.value || undefined,
      status: (status.value || undefined) as StockStatus | undefined,
      q: appliedKeyword.value || undefined,
    })
    if (seq === requestSeq) products.value = list
  } catch (err) {
    if (seq === requestSeq) message.error(err instanceof Error ? err.message : String(err))
  } finally {
    if (seq === requestSeq) {
      loading.value = false
      loaded.value = true
    }
  }
}

let keywordTimer: ReturnType<typeof setTimeout> | null = null
let liveTimer: ReturnType<typeof setTimeout> | null = null
watch(keyword, (value) => {
  if (keywordTimer) clearTimeout(keywordTimer)
  keywordTimer = setTimeout(() => {
    keywordTimer = null
    appliedKeyword.value = value.trim()
  }, KEYWORD_DEBOUNCE_MS)
})
watch([siteId, status, appliedKeyword], () => void loadProducts())
watch(view, (value) => {
  try {
    localStorage.setItem(VIEW_STORAGE_KEY, value)
  } catch {
    // 忽略
  }
})

onLive('event', (event) => {
  if (!refreshEventTypes.includes(event.type)) return
  if (liveTimer) clearTimeout(liveTimer)
  liveTimer = setTimeout(() => {
    liveTimer = null
    void loadProducts()
  }, LIVE_DEBOUNCE_MS)
})

onMounted(() => {
  void loadSites()
  void loadProducts()
})
onBeforeUnmount(() => {
  if (keywordTimer) clearTimeout(keywordTimer)
  if (liveTimer) clearTimeout(liveTimer)
})

function openWatch(product: Product): void {
  drawerProduct.value = product
  drawerShow.value = true
}

function onSaved(): void {
  void loadProducts()
}

const rowKey = (row: Product): string => `${row.site_id}/${row.id}`

function renderName(row: Product) {
  const name = row.url
    ? h('a', { href: row.url, target: '_blank', rel: 'noopener noreferrer', style: 'color: inherit' }, row.name)
    : h('span', row.name)
  const extra = row.no_discount ? h(NTag, { size: 'tiny', type: 'warning', bordered: false, style: 'margin-left:6px' }, () => '优惠不可用') : null
  return h('div', [name, extra])
}

function renderPrices(row: Product) {
  if (row.prices.length === 0) return h(NText, { depth: 3 }, () => '-')
  return h(
    'div',
    { style: 'display: flex; flex-direction: column; gap: 2px' },
    row.prices.map((price) =>
      h('div', { key: price.cycle, style: 'display: flex; justify-content: space-between; gap: 12px' }, [
        h(NText, { depth: 3 }, () => h(CycleLabel, { cycle: price.cycle })),
        h(MoneyText, { money: price.amount, strong: true }),
      ]),
    ),
  )
}

function renderWatchAction(row: Product) {
  if (!row.watched) return h(NButton, { size: 'small', type: 'primary', secondary: true, onClick: () => openWatch(row) }, () => '关注')
  return h(NSpace, { size: 4, align: 'center', wrap: false }, () => [
    h(NTag, { size: 'small', type: 'success', bordered: false }, () => '已关注'),
    h(NButton, { size: 'tiny', text: true, type: 'primary', onClick: () => openWatch(row) }, () => '再加一条'),
  ])
}

const isMobile = useIsMobile()
const columns = computed<DataTableColumns<Product>>(() => [
  { title: '商品', key: 'name', minWidth: 220, render: renderName },
  { title: '站点', key: 'site_id', width: 120, render: (row) => siteNames.value[row.site_id] ?? row.site_id },
  { title: '库存', key: 'stock', width: 100, render: (row) => h(StockBadge, { stock: row.stock }) },
  { title: '规格', key: 'specs', minWidth: 200, render: (row) => h(SpecLine, { specs: row.specs }) },
  { title: '价格', key: 'prices', width: 170, render: renderPrices },
  { title: '更新', key: 'fetched_at', width: 90, render: (row) => h(TimeAgo, { value: row.fetched_at }) },
  { title: '操作', key: 'actions', width: 140, fixed: isMobile.value ? undefined : 'right', render: renderWatchAction },
])
</script>

<template>
  <div class="page">
    <page-header title="商品" :subtitle="loaded ? `共 ${products.length} 个` : undefined">
      <n-radio-group v-model:value="view" size="small">
        <n-radio-button value="cards">卡片</n-radio-button>
        <n-radio-button value="table">表格</n-radio-button>
      </n-radio-group>
    </page-header>

    <div class="filters">
      <n-select v-model:value="siteId" :options="siteOptions" placeholder="全部站点" class="filter" />
      <n-select v-model:value="status" :options="statusOptions" placeholder="全部状态" class="filter" />
      <n-input v-model:value="keyword" placeholder="搜索商品名称" clearable class="filter keyword" />
    </div>

    <n-spin :show="loading">
      <n-empty v-if="loaded && products.length === 0" class="empty" :description="hasFilter ? '没有符合条件的商品' : '暂无商品'">
        <template v-if="!hasFilter" #extra>
          <n-text depth="3">商品会在站点首次轮询后出现，</n-text>
          <router-link to="/sites">去站点页添加或立即轮询</router-link>
        </template>
      </n-empty>

      <div v-else-if="view === 'cards'" class="grid-cards">
        <n-card v-for="product in sortedProducts" :key="rowKey(product)" size="small" class="product-card">
          <template #header>
            <a v-if="product.url" :href="product.url" target="_blank" rel="noopener noreferrer" class="product-link">{{ product.name }}</a>
            <span v-else>{{ product.name }}</span>
          </template>
          <template #header-extra>
            <stock-badge :stock="product.stock" />
          </template>
          <n-space vertical :size="8">
            <n-space :size="6" align="center">
              <n-text depth="3" style="font-size: 12px">{{ siteNames[product.site_id] ?? product.site_id }}</n-text>
              <n-tag v-if="product.no_discount" size="tiny" type="warning" :bordered="false">优惠不可用</n-tag>
            </n-space>
            <spec-line :specs="product.specs" />
            <div class="price-list">
              <div v-for="price in product.prices" :key="price.cycle" class="price-row">
                <n-text depth="3"><cycle-label :cycle="price.cycle" /></n-text>
                <money-text :money="price.amount" strong />
              </div>
              <n-text v-if="product.prices.length === 0" depth="3">暂无价格</n-text>
            </div>
          </n-space>
          <template #footer>
            <div class="card-footer">
              <n-text depth="3" style="font-size: 12px">更新于 <time-ago :value="product.fetched_at" /></n-text>
              <n-button v-if="!product.watched" size="small" type="primary" secondary @click="openWatch(product)">关注</n-button>
              <n-space v-else :size="4" align="center" :wrap="false">
                <n-tag size="small" type="success" :bordered="false">已关注</n-tag>
                <n-button size="tiny" text type="primary" @click="openWatch(product)">再加一条</n-button>
              </n-space>
            </div>
          </template>
        </n-card>
      </div>

      <n-data-table v-else :columns="columns" :data="sortedProducts" :row-key="rowKey" :scroll-x="900" size="small" :bordered="false" />
    </n-spin>

    <watch-form-drawer v-model:show="drawerShow" :product="drawerProduct" @saved="onSaved" />
  </div>
</template>

<style scoped>
.filters {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 14px;
}
.filter {
  width: 160px;
}
.filter.keyword {
  width: 220px;
}
@media (max-width: 640px) {
  .filter,
  .filter.keyword {
    width: 100%;
  }
}
.empty {
  padding: 48px 0;
}
.product-card :deep(.n-card-header__main) {
  min-width: 0;
  word-break: break-word;
}
.product-link {
  color: inherit;
  text-decoration: none;
}
.product-link:hover {
  text-decoration: underline;
}
.price-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.price-row {
  display: flex;
  justify-content: space-between;
  gap: 12px;
}
.card-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
</style>
