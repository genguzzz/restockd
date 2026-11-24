<script setup lang="ts">
// 商品页：站点 / 库存多选筛选（窄屏芯片、宽屏侧栏，状态存在 URL）+ 关键词，卡片或表格视图；点商品下单，一键关注、立即刷新
import { useIsMobile } from '@/composables/useIsMobile'
import { computed, h, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import {
  NButton,
  NCard,
  NDataTable,
  NEmpty,
  NInput,
  NRadioButton,
  NRadioGroup,
  NSpace,
  NSpin,
  NTag,
  NText,
  useMessage,
  type DataTableColumns,
} from 'naive-ui'
import { productsApi, sitesApi } from '@/api'
import type { EventType, Product, StockStatus } from '@/api/types'
import { errorText, useAction, useBusy } from '@/composables/useAsync'
import { onLive } from '@/composables/useLive'
import { useProductHiding } from '@/composables/useProductHiding'
import { useSites } from '@/composables/useSites'
import { stockMeta } from '@/utils/labels'
import CycleLabel from '@/components/CycleLabel.vue'
import FilterGroup, { type FilterOption } from '@/components/FilterGroup.vue'
import LastOrderTag from '@/components/LastOrderTag.vue'
import MoneyText from '@/components/MoneyText.vue'
import PageHeader from '@/components/PageHeader.vue'
import ProductOrderDrawer from '@/components/ProductOrderDrawer.vue'
import SpecLine from '@/components/SpecLine.vue'
import StockBadge from '@/components/StockBadge.vue'
import TimeAgo from '@/components/TimeAgo.vue'
import WatchFormDrawer from '@/components/WatchFormDrawer.vue'

type ViewMode = 'cards' | 'table'

const KEYWORD_DEBOUNCE_MS = 300
const LIVE_DEBOUNCE_MS = 1000
const VIEW_STORAGE_KEY = 'restockd.products.view'
// URL 里没有 stock 参数时默认只看有货；stock= 空值表示全部
const DEFAULT_STOCK: StockStatus[] = ['in_stock']
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
const act = useAction()
const { isBusy, track } = useBusy()
const route = useRoute()
const router = useRouter()
const isMobile = useIsMobile()
const { sites, siteNames } = useSites()
const { hide, unhide } = useProductHiding()
const view = ref<ViewMode>(readView())
const products = ref<Product[]>([])
const loading = ref(false)
const loaded = ref(false)
const watchShow = ref(false)
const watchProduct = ref<Product | null>(null)
const orderShow = ref(false)
const orderProduct = ref<Product | null>(null)

// ---------- 筛选（与 URL query 双向同步） ----------
const listParam = (value: unknown): string[] | null => (typeof value === 'string' ? value.split(',').filter(Boolean) : null)
const siteFilter = computed<string[]>({
  get: () => listParam(route.query.site) ?? [],
  set: (value) => setQuery({ site: value.length ? value.join(',') : undefined }),
})
const stockFilter = computed<string[]>({
  get: () => listParam(route.query.stock) ?? DEFAULT_STOCK,
  set: (value) => setQuery({ stock: value.join(',') }),
})
// 「已屏蔽」开关：?hidden=1 时连同屏蔽商品一起加载，且只显示屏蔽的
const HIDDEN = 'hidden'
const hiddenOnly = computed(() => route.query.hidden === '1')
const hiddenToggle = computed<string[]>({
  get: () => (hiddenOnly.value ? [HIDDEN] : []),
  set: (value) => setQuery({ hidden: value.includes(HIDDEN) ? '1' : undefined }),
})
const hiddenOptions = computed<FilterOption[]>(() => [
  { value: HIDDEN, label: '已屏蔽', count: sites.value.reduce((sum, site) => sum + (site.hidden_count ?? 0), 0) },
])
const keyword = ref(typeof route.query.q === 'string' ? route.query.q : '')
const appliedKeyword = computed(() => (typeof route.query.q === 'string' ? route.query.q : ''))
function setQuery(patch: Record<string, string | undefined>): void {
  void router.replace({ query: { ...route.query, ...patch } })
}

const siteOptions = computed<FilterOption[]>(() =>
  sites.value.map((site) => ({ value: site.id, label: site.name, count: `${site.in_stock_count}/${site.product_count}` })),
)
const stockOptions: FilterOption[] = (Object.keys(stockMeta) as StockStatus[]).map((key) => ({ value: key, label: stockMeta[key].label }))
const hasFilter = computed(() => siteFilter.value.length > 0 || stockFilter.value.length > 0 || Boolean(appliedKeyword.value) || hiddenOnly.value)

// 站点与库存在本地过滤（支持多选；看已屏蔽时不按库存过滤）；可下单（有货 / 预售）优先，其次按站点、名称
const visibleProducts = computed<Product[]>(() => {
  const siteSet = new Set(siteFilter.value)
  const stockSet = new Set(hiddenOnly.value ? [] : stockFilter.value)
  const rank = (item: Product): number => (orderableStatuses.includes(item.stock.status) ? 0 : 1)
  return products.value
    .filter((item) => Boolean(item.hidden) === hiddenOnly.value)
    .filter((item) => (siteSet.size === 0 || siteSet.has(item.site_id)) && (stockSet.size === 0 || stockSet.has(item.stock.status)))
    .sort((a, b) => {
      const byRank = rank(a) - rank(b)
      if (byRank !== 0) return byRank
      const bySite = (siteNames.value[a.site_id] ?? a.site_id).localeCompare(siteNames.value[b.site_id] ?? b.site_id)
      if (bySite !== 0) return bySite
      return a.name.localeCompare(b.name)
    })
})

let requestSeq = 0
async function loadProducts(): Promise<void> {
  const seq = ++requestSeq
  loading.value = true
  try {
    const list = await productsApi.list({ q: appliedKeyword.value || undefined, include_hidden: hiddenOnly.value ? 1 : undefined })
    if (seq === requestSeq) products.value = list
  } catch (err) {
    if (seq === requestSeq) message.error(errorText(err))
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
    setQuery({ q: value.trim() || undefined })
  }, KEYWORD_DEBOUNCE_MS)
})
watch([appliedKeyword, hiddenOnly], () => void loadProducts())
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
  void loadProducts()
})
onBeforeUnmount(() => {
  if (keywordTimer) clearTimeout(keywordTimer)
  if (liveTimer) clearTimeout(liveTimer)
})

// 立即轮询商品所在站点并刷新列表（同站点的商品共用加载态）
async function pollSite(product: Product): Promise<void> {
  const result = await track(product.site_id, () => act(() => sitesApi.poll(product.site_id)))
  if (!result) return
  message.success(`已刷新，共 ${result.products} 个商品`)
  void loadProducts()
}

// 屏蔽后立即从列表移除；撤销 / 恢复后重新加载
async function hideProduct(product: Product): Promise<void> {
  if (!(await hide(product.site_id, product.id, () => void loadProducts()))) return
  products.value = products.value.filter((item) => rowKey(item) !== rowKey(product))
}

async function restoreProduct(product: Product): Promise<void> {
  if (await unhide(product.site_id, product.id, '已恢复')) void loadProducts()
}

function openWatch(product: Product): void {
  watchProduct.value = product
  watchShow.value = true
}

function openOrder(product: Product): void {
  orderProduct.value = product
  orderShow.value = true
}

const rowKey = (row: Product): string => `${row.site_id}/${row.id}`

function renderName(row: Product) {
  const name = h('span', { class: 'product-link', onClick: () => openOrder(row) }, row.name)
  const tags = [
    row.no_discount ? h(NTag, { size: 'tiny', type: 'warning', bordered: false }, () => '优惠不可用') : null,
    row.last_order ? h(LastOrderTag, { order: row.last_order }) : null,
  ]
  return h('div', [name, h(NSpace, { size: 4, style: 'margin-top: 2px' }, () => tags)])
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

function renderActions(row: Product) {
  if (row.hidden) return h(NButton, { size: 'small', onClick: () => void restoreProduct(row) }, () => '恢复')
  return h(NSpace, { size: 4, align: 'center', wrap: false }, () => [
    h(NButton, { size: 'small', type: 'warning', secondary: true, onClick: () => openOrder(row) }, () => '下单'),
    row.watched
      ? h(NButton, { size: 'tiny', text: true, type: 'primary', onClick: () => openWatch(row) }, () => '再加关注')
      : h(NButton, { size: 'small', type: 'primary', secondary: true, onClick: () => openWatch(row) }, () => '关注'),
    h(NButton, { size: 'tiny', text: true, loading: isBusy(row.site_id), onClick: () => void pollSite(row) }, () => '立即刷新'),
    h(NButton, { size: 'tiny', text: true, onClick: () => void hideProduct(row) }, () => '屏蔽'),
  ])
}

const columns = computed<DataTableColumns<Product>>(() => [
  { title: '商品', key: 'name', minWidth: 220, render: renderName },
  { title: '站点', key: 'site_id', width: 120, render: (row) => siteNames.value[row.site_id] ?? row.site_id },
  { title: '库存', key: 'stock', width: 100, render: (row) => h(StockBadge, { stock: row.stock }) },
  { title: '规格', key: 'specs', minWidth: 200, render: (row) => h(SpecLine, { specs: row.specs }) },
  { title: '价格', key: 'prices', width: 170, render: renderPrices },
  { title: '更新', key: 'fetched_at', width: 90, render: (row) => h(TimeAgo, { value: row.fetched_at }) },
  { title: '操作', key: 'actions', width: 270, fixed: isMobile.value ? undefined : 'right', render: renderActions },
])
</script>

<template>
  <div class="page">
    <page-header title="商品" :subtitle="loaded ? `显示 ${visibleProducts.length} / ${products.length} 个` : undefined">
      <n-radio-group v-model:value="view" size="small">
        <n-radio-button value="cards">卡片</n-radio-button>
        <n-radio-button value="table">表格</n-radio-button>
      </n-radio-group>
    </page-header>

    <div :class="{ layout: !isMobile }">
      <aside :class="isMobile ? 'filters-mobile' : 'filters-side'">
        <FilterGroup v-model:value="siteFilter" title="站点" :options="siteOptions" />
        <FilterGroup v-if="!hiddenOnly" v-model:value="stockFilter" title="库存" :options="stockOptions" />
        <FilterGroup v-model:value="hiddenToggle" title="其他" :options="hiddenOptions" :show-all="false" />
        <n-input v-if="isMobile" v-model:value="keyword" placeholder="搜索商品名称" clearable />
      </aside>

      <main class="content">
        <n-input v-if="!isMobile" v-model:value="keyword" placeholder="搜索商品名称" clearable class="keyword" />
        <n-spin :show="loading">
          <n-empty v-if="loaded && visibleProducts.length === 0" class="empty" :description="hasFilter ? '没有符合条件的商品' : '暂无商品'">
            <template v-if="!hasFilter" #extra>
              <n-text depth="3">商品会在站点首次轮询后出现，</n-text>
              <router-link to="/sites">去站点页添加或立即轮询</router-link>
            </template>
          </n-empty>

          <div v-else-if="view === 'cards'" class="grid-cards">
            <n-card v-for="product in visibleProducts" :key="rowKey(product)" size="small" hoverable class="product-card">
              <template #header>
                <span class="product-link" @click="openOrder(product)">{{ product.name }}</span>
              </template>
              <template #header-extra>
                <stock-badge :stock="product.stock" />
              </template>
              <n-space vertical :size="8" class="body" @click="openOrder(product)">
                <n-space :size="6" align="center">
                  <n-text depth="3" style="font-size: 12px">{{ siteNames[product.site_id] ?? product.site_id }}</n-text>
                  <n-tag v-if="product.no_discount" size="tiny" type="warning" :bordered="false">优惠不可用</n-tag>
                  <a v-if="product.url" :href="product.url" target="_blank" rel="noopener noreferrer" class="external" @click.stop>商品页</a>
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
              <LastOrderTag v-if="product.last_order" :order="product.last_order" class="last-order" />
              <template #footer>
                <div class="card-footer">
                  <n-text depth="3" style="font-size: 12px">
                    更新于 <time-ago :value="product.fetched_at" />
                    <n-button text size="tiny" :loading="isBusy(product.site_id)" style="margin-left: 6px" @click="pollSite(product)">立即刷新</n-button>
                  </n-text>
                  <n-button v-if="product.hidden" size="small" @click="restoreProduct(product)">恢复</n-button>
                  <n-space v-else :size="4" align="center" :wrap="false">
                    <n-button size="tiny" text @click="hideProduct(product)">屏蔽</n-button>
                    <n-button size="small" type="warning" secondary @click="openOrder(product)">下单</n-button>
                    <n-button v-if="!product.watched" size="small" type="primary" secondary @click="openWatch(product)">关注</n-button>
                    <n-button v-else size="tiny" text type="primary" @click="openWatch(product)">再加关注</n-button>
                  </n-space>
                </div>
              </template>
            </n-card>
          </div>

          <n-data-table v-else :columns="columns" :data="visibleProducts" :row-key="rowKey" :scroll-x="1000" size="small" :bordered="false" />
        </n-spin>
      </main>
    </div>

    <watch-form-drawer v-model:show="watchShow" :product="watchProduct" @saved="loadProducts" />
    <ProductOrderDrawer v-model:show="orderShow" :product="orderProduct" @ordered="loadProducts" @hide="hideProduct" />
  </div>
</template>

<style scoped>
.layout {
  display: grid;
  grid-template-columns: 200px minmax(0, 1fr);
  gap: 16px;
  align-items: start;
}
.filters-side {
  position: sticky;
  top: 12px;
  display: flex;
  flex-direction: column;
  gap: 18px;
  max-height: calc(100vh - 100px);
  overflow-y: auto;
}
.filters-mobile {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-bottom: 12px;
}
.keyword {
  margin-bottom: 12px;
  max-width: 320px;
}
.empty {
  padding: 48px 0;
}
.product-card :deep(.n-card-header__main) {
  min-width: 0;
  word-break: break-word;
}
:deep(.product-link),
.body {
  cursor: pointer;
}
:deep(.product-link:hover) {
  text-decoration: underline;
}
.external {
  font-size: 12px;
}
.last-order {
  margin-top: 8px;
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
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
</style>
