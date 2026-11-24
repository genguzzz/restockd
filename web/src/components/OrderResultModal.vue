<script setup lang="ts">
// 下单 / 试下单结果：状态、金额、订单与账单号、说明、错误；已锁单时可去付款或取消订单
import { computed } from 'vue'
import { NButton, NDescriptions, NDescriptionsItem, NModal, NSpace, NTag, NText } from 'naive-ui'
import type { Order } from '@/api/types'
import { metaOf, orderStatusMeta } from '@/utils/labels'
import MoneyText from './MoneyText.vue'
import CancelOrderButton from './CancelOrderButton.vue'

const props = defineProps<{ order: Order | null }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'cancelled', order: Order): void }>()
const status = computed(() => (props.order ? metaOf(orderStatusMeta, props.order.status) : null))
</script>

<template>
  <n-modal
    :show="order !== null"
    preset="card"
    title="下单结果"
    style="width: min(480px, 94vw)"
    @update:show="(value: boolean) => !value && emit('close')"
  >
    <n-descriptions v-if="order && status" :column="1" label-placement="left" bordered size="small">
      <n-descriptions-item label="状态">
        <n-tag size="small" :type="status.type" :bordered="false">{{ status.label }}</n-tag>
      </n-descriptions-item>
      <n-descriptions-item label="金额"><MoneyText :money="order.result.total" strong /></n-descriptions-item>
      <n-descriptions-item v-if="order.result.order_id" label="订单号">{{ order.result.order_id }}</n-descriptions-item>
      <n-descriptions-item v-if="order.result.invoice_id" label="账单号">{{ order.result.invoice_id }}</n-descriptions-item>
      <n-descriptions-item v-if="order.result.message" label="说明">{{ order.result.message }}</n-descriptions-item>
      <n-descriptions-item v-if="order.error" label="错误">
        <n-text type="error" style="word-break: break-all">{{ order.error }}</n-text>
      </n-descriptions-item>
      <n-descriptions-item label="耗时">{{ order.duration_ms }} ms</n-descriptions-item>
    </n-descriptions>
    <template v-if="order?.status === 'placed'" #footer>
      <n-space justify="end">
        <CancelOrderButton :order="order" size="medium" @cancelled="(value: Order) => emit('cancelled', value)" />
        <n-button v-if="order.result.pay_url" tag="a" type="primary" :href="order.result.pay_url" target="_blank" rel="noopener noreferrer">
          去付款
        </n-button>
      </n-space>
    </template>
  </n-modal>
</template>
