<script setup lang="ts">
// 库存状态徽标（含数量）
import { computed } from 'vue'
import { NTag } from 'naive-ui'
import type { Stock } from '@/api/types'
import { metaOf, stockMeta } from '@/utils/labels'

const props = defineProps<{ stock: Stock; size?: 'small' | 'medium' }>()
const meta = computed(() => metaOf(stockMeta, props.stock.status))
const text = computed(() => {
  const label = meta.value.label
  if (props.stock.qty !== undefined && props.stock.qty !== null && props.stock.status !== 'out_of_stock') return `${label} ${props.stock.qty}`
  return label
})
</script>

<template>
  <n-tag :type="meta.type" :size="size ?? 'small'" round :bordered="false" :title="stock.reason || undefined">{{ text }}</n-tag>
</template>
