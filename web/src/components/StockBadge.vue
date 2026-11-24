<script setup lang="ts">
// 库存状态徽标（含数量）
import { computed } from 'vue'
import { NTag } from 'naive-ui'
import type { Stock } from '@/api/types'
import { stockLabels } from '@/utils/format'

const props = defineProps<{ stock: Stock; size?: 'small' | 'medium' }>()
const type = computed(() => {
  switch (props.stock.status) {
    case 'in_stock':
      return 'success'
    case 'preorder':
      return 'info'
    case 'out_of_stock':
      return 'error'
    case 'closed':
      return 'warning'
    default:
      return 'default'
  }
})
const text = computed(() => {
  const label = stockLabels[props.stock.status] ?? props.stock.status
  if (props.stock.qty !== undefined && props.stock.qty !== null && props.stock.status !== 'out_of_stock') return `${label} ${props.stock.qty}`
  return label
})
</script>

<template>
  <n-tag :type="type" :size="size ?? 'small'" round :bordered="false" :title="stock.reason || undefined">{{ text }}</n-tag>
</template>
