<script setup lang="ts">
// 关注与锁单页：关注规则列表、内联开关、算价 / 试下单 / 立即锁单 / 编辑 / 删除
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
import { accountsApi, productsApi, sitesApi, watchesApi } from '@/api'
import type { Account, Order, OrderStatus, Product, Quote, Site, Watch, WatchInput } from '@/api/types'
import { onLive } from '@/composables/useLive'
import ConfirmButton from '@/components/ConfirmButton.vue'
import CycleLabel from '@/components/CycleLabel.vue'
import MoneyText from '@/components/MoneyText.vue'
import PageHeader from '@/components/PageHeader.vue'
import TimeAgo from '@/components/TimeAgo.vue'
import WatchFormDrawer from '@/components/WatchFormDrawer.vue'

type ToggleField = 'auto_order' | 'enabled'

const orderStatusLabels: Record<OrderStatus, string> = { pending: '进行中', placed: '已下单', dry_run: '试下单', failed: '失败' }
const orderStatusTypes: Record<OrderStatus, 'default' | 'success' | 'info' | 'error'> = {
  pending: 'default',
  placed: 'success',
  dry_run: 'info',
  failed: 'error',
}

const message = useMessage()
const watches = ref<Watch[]>([])
const sites = ref<Site[]>([])
const accounts = ref<Account[]>([])
const products = ref<Product[]>([])
const loading = ref(false)
const toggling = ref<Record<string, boolean>>({})
const busy = ref<Record<string, boolean>>({})
const drawerShow = ref(false)
const editing = ref<Watch | null>(null)
const quoteShow = ref(false)
const quote = ref<Quote | null>(null)
const orderShow = ref(false)
const order = ref<Order | null>(null)

const siteNames = computed<Record<string, string>>(() => Object.fromEntries(sites.value.map((site) => [site.id, site.name])))
const accountLabels = computed<Record<string, string>>(() => Object.fromEntries(accounts.value.map((item) => [item.id, item.label])))
const productMap = computed<Map<string, Product>>(() => new Map(products.value.map((item) => [`${item.site_id}/${item.id}`, item])))

const errorMessage = (err: unknown): string => (err instanceof Error ? err.message : String(err))
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

async function loadWatches(): Promise<void> {
  loading.value = true
  try {
    watches.value = await watchesApi.list()
  } catch (err) {
    message.error(errorMessage(err))
  } finally {
    loading.value = false
  }
}

async function loadLookups(): Promise<void> {
  const [siteList, accountList, productList] = await Promise.allSettled([sitesApi.list(), accountsApi.list(), productsApi.list()])
  if (siteList.status === 'fulfilled') sites.value = siteList.value
  if (accountList.status === 'fulfilled') accounts.value = accountList.value
  if (productList.status === 'fulfilled') products.value = productList.value
}

function replaceWatch(saved: Watch): void {
  const exists = watches.value.some((item) => item.id === saved.id)
  watches.value = exists ? watches.value.map((item) => (item.id === saved.id ? saved : item)) : [saved, ...watches.value]
}

// 内联开关：先乐观更新，失败回滚
async function toggle(row: Watch, field: ToggleField, value: boolean): Promise<void> {
  if (field === 'auto_order' && value && !row.account_id) {
    message.warning('请先编辑该关注并选择下单账号')
    return
  }
  const previous = row[field]
  const key = `${row.id}:${field}`
  toggling.value = { ...toggling.value, [key]: true }
  replaceWatch({ ...row, [field]: value })
  try {
    const saved = await watchesApi.update(row.id, { ...toInput(row), [field]: value })
    replaceWatch(saved)
  } catch (err) {
    replaceWatch({ ...row, [field]: previous })
    message.error(errorMessage(err))
  } finally {
    toggling.value = { ...toggling.value, [key]: false }
  }
}

async function withBusy<T>(key: string, fn: () => Promise<T>): Promise<T | null> {
  busy.value = { ...busy.value, [key]: true }
  try {
    return await fn()
  } catch (err) {
    message.error(errorMessage(err))
    return null
  } finally {
    busy.value = { ...busy.value, [key]: false }
  }
}

async function runQuote(row: Watch): Promise<void> {
  const result = await withBusy(`${row.id}:quote`, () => watchesApi.quote(row.id))
  if (!result) return
  quote.value = result
  quoteShow.value = true
}

async function runOrder(row: Watch, dryRun: boolean): Promise<void> {
  const result = await withBusy(`${row.id}:${dryRun ? 'dry' : 'order'}`, () => watchesApi.order(row.id, dryRun))
  if (!result) return
  order.value = result
  orderShow.value = true
  if (!dryRun) void loadWatches()
}

async function removeWatch(row: Watch): Promise<void> {
  try {
    await watchesApi.remove(row.id)
    watches.value = watches.value.filter((item) => item.id !== row.id)
    message.success('已删除')
  } catch (err) {
    message.error(errorMessage(err))
  }
}

function openCreate(): void {
  editing.value = null
  drawerShow.value = true
}

function openEdit(row: Watch): void {
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
        loading: toggling.value[`${row.id}:auto_order`] === true,
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
        loading: toggling.value[`${row.id}:enabled`] === true,
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
        h(NButton, { size: 'small', onClick: () => openEdit(row) }, () => '编辑'),
        h(NButton, { size: 'small', secondary: true, loading: busy.value[`${row.id}:quote`] === true, onClick: () => void runQuote(row) }, () => '算价'),
        h(
          NButton,
          { size: 'small', secondary: true, type: 'info', loading: busy.value[`${row.id}:dry`] === true, onClick: () => void runOrder(row, true) },
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
      <n-button type="primary" @click="openCreate">新建关注</n-button>
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
            <n-button size="small" type="primary" secondary @click="openCreate">新建关注</n-button>
          </n-space>
        </template>
      </n-data-table>
    </n-card>

    <watch-form-drawer v-model:show="drawerShow" :watch="editing" @saved="onSaved" />

    <n-modal v-model:show="quoteShow" preset="card" title="算价结果" style="width: min(420px, 94vw)">
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

    <n-modal v-model:show="orderShow" preset="card" title="下单结果" style="width: min(480px, 94vw)">
      <n-descriptions v-if="order" :column="1" label-placement="left" bordered size="small">
        <n-descriptions-item label="状态">
          <n-tag size="small" :type="orderStatusTypes[order.status] ?? 'default'" :bordered="false">
            {{ orderStatusLabels[order.status] ?? order.status }}
          </n-tag>
        </n-descriptions-item>
        <n-descriptions-item label="金额"><money-text :money="order.result.total" strong /></n-descriptions-item>
        <n-descriptions-item v-if="order.result.order_id" label="订单号">{{ order.result.order_id }}</n-descriptions-item>
        <n-descriptions-item v-if="order.result.invoice_id" label="账单号">{{ order.result.invoice_id }}</n-descriptions-item>
        <n-descriptions-item v-if="order.result.message" label="说明">{{ order.result.message }}</n-descriptions-item>
        <n-descriptions-item v-if="order.error" label="错误">
          <n-text type="error" style="word-break: break-all">{{ order.error }}</n-text>
        </n-descriptions-item>
        <n-descriptions-item label="耗时">{{ order.duration_ms }} ms</n-descriptions-item>
      </n-descriptions>
      <template v-if="order?.result.pay_url" #footer>
        <n-space justify="end">
          <n-button tag="a" type="primary" :href="order.result.pay_url" target="_blank" rel="noopener noreferrer">去支付</n-button>
        </n-space>
      </template>
    </n-modal>
  </div>
</template>
