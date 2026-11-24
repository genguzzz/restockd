<script setup lang="ts">
// 商品最近一次下单的标记：已锁单（可去付款）/ 已试下单 / 上次失败（悬停看原因），防止重复下单
import { computed } from 'vue'
import { NButton, NSpace, NTag, NTooltip } from 'naive-ui'
import type { LastOrder } from '@/api/types'
import { metaOf, orderStatusMeta, type TagMeta } from '@/utils/labels'
import TimeAgo from './TimeAgo.vue'

const props = defineProps<{ order: LastOrder; size?: 'small' | 'medium' }>()

const labels: Partial<Record<LastOrder['status'], string>> = { placed: '已锁单', dry_run: '已试下单', failed: '上次失败', cancelled: '订单已取消' }
const meta = computed<TagMeta>(() => {
  const base = metaOf(orderStatusMeta, props.order.status)
  return { ...base, label: labels[props.order.status] ?? base.label }
})
</script>

<template>
  <n-space :size="6" align="center" :wrap="false">
    <n-tooltip :disabled="!order.error">
      <template #trigger>
        <n-tag :type="meta.type" :size="size ?? 'small'" :bordered="false">
          {{ meta.label }}<template v-if="order.status !== 'failed'"> · <TimeAgo :value="order.created_at" /></template>
        </n-tag>
      </template>
      {{ order.error }}
    </n-tooltip>
    <n-button
      v-if="order.status === 'placed' && order.pay_url"
      tag="a"
      :href="order.pay_url"
      target="_blank"
      rel="noopener noreferrer"
      size="tiny"
      type="primary"
      text
    >
      去付款
    </n-button>
  </n-space>
</template>
