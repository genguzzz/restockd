<script setup lang="ts">
// 订单页：锁单记录表格，实时推送新订单 / 状态变化
import { computed, h, onMounted, ref } from 'vue'
import { NButton, NCard, NDataTable, NEllipsis, NIcon, NTag, NText, type DataTableColumns } from 'naive-ui'
import { OpenOutline, RefreshOutline } from '@vicons/ionicons5'
import { ordersApi } from '@/api'
import type { Order } from '@/api/types'
import { useAsync } from '@/composables/useAsync'
import { onLive } from '@/composables/useLive'
import { useSites } from '@/composables/useSites'
import { formatDateTime } from '@/utils/format'
import { metaOf, orderStatusMeta } from '@/utils/labels'
import { upsertById } from '@/utils/list'
import PageHeader from '@/components/PageHeader.vue'
import MoneyText from '@/components/MoneyText.vue'
import CycleLabel from '@/components/CycleLabel.vue'

const orders = ref<Order[]>([])
const { siteNames } = useSites()
const { loading, run } = useAsync(async () => {
  orders.value = await ordersApi.list(200)
  return orders.value
})

// 实时推送：按 id 更新，新订单插到最前
onLive('order', (order) => {
  orders.value = upsertById(orders.value, order, { prepend: true })
})

const ellipsis = (text: string, maxWidth: number) => h(NEllipsis, { style: `max-width: ${maxWidth}px` }, { default: () => text })

const columns = computed<DataTableColumns<Order>>(() => [
  { title: '时间', key: 'created_at', width: 160, render: (row) => formatDateTime(row.created_at) },
  { title: '站点', key: 'site_id', width: 110, render: (row) => siteNames.value[row.site_id] ?? row.site_id },
  { title: '商品', key: 'product_name', minWidth: 180, render: (row) => ellipsis(row.product_name || row.product_id, 260) },
  {
    title: '周期 × 数量',
    key: 'cycle',
    width: 100,
    render: (row) => h('span', null, [h(CycleLabel, { cycle: row.spec.cycle }), ` × ${row.spec.qty}`]),
  },
  { title: '优惠码', key: 'coupon', width: 90, render: (row) => row.spec.coupon || '-' },
  {
    title: '状态',
    key: 'status',
    width: 90,
    render: (row) => {
      const meta = metaOf(orderStatusMeta, row.status)
      return h(NTag, { type: meta.type, size: 'small', bordered: false }, { default: () => meta.label })
    },
  },
  { title: '金额', key: 'total', width: 100, render: (row) => h(MoneyText, { money: row.result?.total, strong: true }) },
  {
    title: '订单 / 账单',
    key: 'order_id',
    width: 120,
    render: (row) => {
      const parts = [row.result?.order_id, row.result?.invoice_id].filter((item): item is string => Boolean(item))
      return parts.length ? parts.join(' / ') : '-'
    },
  },
  {
    title: '支付',
    key: 'pay_url',
    width: 80,
    render: (row) =>
      row.result?.pay_url
        ? h(
            NButton,
            { tag: 'a', href: row.result.pay_url, target: '_blank', rel: 'noopener noreferrer', size: 'tiny', type: 'primary', secondary: true },
            { default: () => '去付款', icon: () => h(NIcon, null, { default: () => h(OpenOutline) }) },
          )
        : '-',
  },
  { title: '耗时', key: 'duration_ms', width: 80, render: (row) => `${row.duration_ms} ms` },
  {
    title: '错误 / 信息',
    key: 'error',
    minWidth: 160,
    render: (row) => {
      if (row.error) return h(NText, { type: 'error' }, { default: () => ellipsis(row.error ?? '', 240) })
      if (row.result?.message) return h(NText, { depth: 3 }, { default: () => ellipsis(row.result.message ?? '', 240) })
      return '-'
    },
  },
])

const pagination = { pageSize: 20 }

onMounted(() => {
  void run()
})
</script>

<template>
  <div class="page">
    <PageHeader title="订单" subtitle="自动 / 手动锁单的执行记录">
      <n-button :loading="loading" @click="run()">
        <template #icon><n-icon><RefreshOutline /></n-icon></template>
        刷新
      </n-button>
    </PageHeader>
    <n-card size="small">
      <n-data-table
        :columns="columns"
        :data="orders"
        :loading="loading"
        :pagination="pagination"
        :row-key="(row: Order) => row.id"
        :scroll-x="1000"
        size="small"
      />
    </n-card>
  </div>
</template>
