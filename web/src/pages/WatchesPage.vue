<script setup lang="ts">
// 关注与锁单页：关注规则列表、内联开关、算价 / 试下单 / 立即锁单（防重复）/ 编辑 / 删除
import { useIsMobile } from '@/composables/useIsMobile'
import { computed, h, onMounted, ref } from 'vue'
import {
  NButton,
  NCard,
  NDataTable,
  NDescriptions,
  NDescriptionsItem,
  NModal,
  NSpace,
  NSwitch,
  NTag,
  NText,
  useMessage,
  type DataTableColumns,
} from 'naive-ui'
import { accountsApi, productsApi, watchesApi } from '@/api'
import type { Account, Order, Product, Quote, Watch, WatchInput } from '@/api/types'
import { useAction, useAsync, useBusy } from '@/composables/useAsync'
import { onLive } from '@/composables/useLive'
import { priorOfProduct, useOrderPlacer } from '@/composables/useOrderPlacer'
import { useSites } from '@/composables/useSites'
import { removeById, upsertById } from '@/utils/list'
import ConfirmButton from '@/components/ConfirmButton.vue'
import CycleLabel from '@/components/CycleLabel.vue'
import MoneyText from '@/components/MoneyText.vue'
import OrderResultModal from '@/components/OrderResultModal.vue'
import PageHeader from '@/components/PageHeader.vue'
import TimeAgo from '@/components/TimeAgo.vue'
import WatchFormDrawer from '@/components/WatchFormDrawer.vue'

type ToggleField = 'auto_order' | 'enabled'

const message = useMessage()
const act = useAction()
const place = useOrderPlacer()
const { isBusy, track } = useBusy()
const { siteNames } = useSites()
const watches = ref<Watch[]>([])
const accounts = ref<Account[]>([])
const products = ref<Product[]>([])
const drawerShow = ref(false)
const editing = ref<Watch | null>(null)
const quote = ref<Quote | null>(null)
const order = ref<Order | null>(null)

const accountLabels = computed<Record<string, string>>(() => Object.fromEntries(accounts.value.map((item) => [item.id, item.label])))
const productMap = computed<Map<string, Product>>(() => new Map(products.value.map((item) => [`${item.site_id}/${item.id}`, item])))

const productOf = (row: Watch): Product | undefined => productMap.value.get(`${row.site_id}/${row.product_id}`)
const currencyOf = (row: Watch): string | null => {
  const product = productOf(row)
  const price = product?.prices.find((item) => item.cycle === row.cycle) ?? product?.prices[0]
  return price?.amount.currency ?? null
}

// 去掉只读字段，得到可提交的请求体
function toInput(row: Watch): WatchInput {
  const { id: _id, orders_placed: _placed, created_at: _created, last_triggered: _triggered, product_name: _name, ...input } = row
  return input
}

const { loading, run: loadWatches } = useAsync(async () => {
  watches.value = await watchesApi.list()
  return watches.value
})

async function loadLookups(): Promise<void> {
  const [accountList, productList] = await Promise.allSettled([accountsApi.list(), productsApi.list()])
  if (accountList.status === 'fulfilled') accounts.value = accountList.value
  if (productList.status === 'fulfilled') products.value = productList.value
}

function replaceWatch(saved: Watch): void {
  watches.value = upsertById(watches.value, saved, { prepend: true })
}

// 内联开关：先乐观更新，失败回滚
async function toggle(row: Watch, field: ToggleField, value: boolean): Promise<void> {
  if (field === 'auto_order' && value && !row.account_id) {
    message.warning('请先编辑该关注并选择下单账号')
    return
  }
  replaceWatch({ ...row, [field]: value })
  const saved = await track(`${row.id}:${field}`, () => act(() => watchesApi.update(row.id, { ...toInput(row), [field]: value })))
  replaceWatch(saved ?? row)
}

async function runQuote(row: Watch): Promise<void> {
  const result = await track(`${row.id}:quote`, () => act(() => watchesApi.quote(row.id)))
  if (result) quote.value = result
}

// 立即锁单已由 ConfirmButton 确认；24 小时内重复锁单或后端 409 时再确认一次并带 force
async function runOrder(row: Watch, dryRun: boolean): Promise<void> {
  const product = productOf(row)
  const result = await track(`${row.id}:${dryRun ? 'dry' : 'order'}`, () =>
    place((force) => watchesApi.order(row.id, dryRun, force), { dryRun, prior: product ? priorOfProduct(product) : null }),
  )
  if (!result) return
  order.value = result
  if (!dryRun) {
    void loadWatches()
    void loadLookups()
  }
}

async function removeWatch(row: Watch): Promise<void> {
  if (await act(() => watchesApi.remove(row.id), '已删除')) watches.value = removeById(watches.value, row.id)
}

function openDrawer(row: Watch | null): void {
  editing.value = row
  drawerShow.value = true
}

function onSaved(saved: Watch): void {
  replaceWatch(saved)
  void loadWatches()
}

onLive('order', () => void loadWatches())

onMounted(() => {
  void loadWatches()
  void loadLookups()
})

const optionsSummary = (row: Watch): string =>
  Object.entries(row.options ?? {})
    .map(([key, value]) => `${key}=${value}`)
    .join(', ')

const isMobile = useIsMobile()
const columns = computed<DataTableColumns<Watch>>(() => [
  {
    title: '商品',
    key: 'product',
    minWidth: 200,
    fixed: isMobile.value ? undefined : 'left',
    render: (row) => {
      const product = productOf(row)
      const name = row.product_name || product?.name || row.product_id
      return h('div', [
        h('div', { style: 'word-break: break-word' }, name),
        h(NText, { depth: 3, style: 'font-size: 12px' }, () => siteNames.value[row.site_id] ?? row.site_id),
      ])
    },
  },
  { title: '周期', key: 'cycle', width: 80, render: (row) => h(CycleLabel, { cycle: row.cycle }) },
  {
    title: '配置',
    key: 'options',
    minWidth: 140,
    render: (row) => {
      const parts = [optionsSummary(row), row.os ? `os=${row.os}` : '', row.qty > 1 ? `×${row.qty}` : ''].filter(Boolean)
      return parts.length ? h(NText, { depth: 2, style: 'font-size: 12px' }, () => parts.join(', ')) : '-'
    },
  },
  { title: '优惠码', key: 'coupon', width: 100, render: (row) => row.coupon || '-' },
  {
    title: '账号',
    key: 'account_id',
    width: 100,
    render: (row) => (row.account_id ? accountLabels.value[row.account_id] ?? row.account_id : h(NText, { depth: 3 }, () => '未设置')),
  },
  {
    title: '自动锁单',
    key: 'auto_order',
    width: 90,
    render: (row) =>
      h(NSwitch, {
        size: 'small',
        value: row.auto_order,
        loading: isBusy(`${row.id}:auto_order`),
        onUpdateValue: (value: boolean) => void toggle(row, 'auto_order', value),
      }),
  },
  {
    title: '启用',
    key: 'enabled',
    width: 70,
    render: (row) =>
      h(NSwitch, {
        size: 'small',
        value: row.enabled,
        loading: isBusy(`${row.id}:enabled`),
        onUpdateValue: (value: boolean) => void toggle(row, 'enabled', value),
      }),
  },
  {
    title: '已锁 / 上限',
    key: 'orders',
    width: 90,
    render: (row) => `${row.orders_placed} / ${row.max_orders > 0 ? row.max_orders : '∞'}`,
  },
  {
    title: '价格上限',
    key: 'max_price_cents',
    width: 100,
    render: (row) => {
      if (!row.max_price_cents) return h(NText, { depth: 3 }, () => '不限')
      return h(MoneyText, { money: { cents: row.max_price_cents, currency: currencyOf(row) ?? '' } })
    },
  },
  { title: '上次触发', key: 'last_triggered', width: 90, render: (row) => h(TimeAgo, { value: row.last_triggered }) },
  {
    title: '操作',
    key: 'actions',
    width: 330,
    fixed: isMobile.value ? undefined : 'right',
    render: (row) =>
      h(NSpace, { size: 6, wrap: true }, () => [
        h(NButton, { size: 'small', onClick: () => openDrawer(row) }, () => '编辑'),
        h(NButton, { size: 'small', secondary: true, loading: isBusy(`${row.id}:quote`), onClick: () => void runQuote(row) }, () => '算价'),
        h(
          NButton,
          { size: 'small', secondary: true, type: 'info', loading: isBusy(`${row.id}:dry`), onClick: () => void runOrder(row, true) },
          () => '试下单',
        ),
        h(ConfirmButton, {
          text: '立即锁单',
          type: 'warning',
          confirm: '将用所选账号在站点创建一笔真实的未支付订单（需在站点内手动付款），确定继续？',
          disabled: !row.account_id,
          action: () => runOrder(row, false),
        }),
        h(ConfirmButton, { text: '删除', type: 'error', secondary: true, confirm: '确定删除这条关注？', action: () => removeWatch(row) }),
      ]),
  },
])
</script>

<template>
  <div class="page">
    <page-header title="关注与锁单" subtitle="补货时通知，并可按规则自动锁单（创建未支付订单）">
      <n-button type="primary" @click="openDrawer(null)">新建关注</n-button>
    </page-header>

    <n-card size="small" :content-style="{ padding: 0 }">
      <n-data-table
        :columns="columns"
        :data="watches"
        :loading="loading"
        :row-key="(row: Watch) => row.id"
        :scroll-x="1400"
        size="small"
        :bordered="false"
      >
        <template #empty>
          <n-space vertical align="center" :size="8" style="padding: 24px 0">
            <n-text depth="3">还没有关注，可在商品页点「关注」或直接新建</n-text>
            <n-button size="small" type="primary" secondary @click="openDrawer(null)">新建关注</n-button>
          </n-space>
        </template>
      </n-data-table>
    </n-card>

    <watch-form-drawer v-model:show="drawerShow" :watch="editing" @saved="onSaved" />

    <n-modal :show="quote !== null" preset="card" title="算价结果" style="width: min(420px, 94vw)" @update:show="(value: boolean) => !value && (quote = null)">
      <n-descriptions v-if="quote" :column="1" label-placement="left" bordered size="small">
        <n-descriptions-item label="小计"><money-text :money="quote.subtotal" /></n-descriptions-item>
        <n-descriptions-item label="优惠"><money-text :money="quote.discount" /></n-descriptions-item>
        <n-descriptions-item label="合计"><money-text :money="quote.total" strong /></n-descriptions-item>
        <n-descriptions-item label="优惠码">
          <n-tag size="small" :type="quote.coupon_valid ? 'success' : 'default'" :bordered="false">
            {{ quote.coupon_valid ? '有效' : '无效 / 未使用' }}
          </n-tag>
        </n-descriptions-item>
        <n-descriptions-item v-if="quote.message" label="说明">{{ quote.message }}</n-descriptions-item>
      </n-descriptions>
    </n-modal>

    <OrderResultModal :order="order" @close="order = null" @cancelled="(value) => (order = value)" />
  </div>
</template>
