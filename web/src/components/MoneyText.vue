<script setup lang="ts">
// 金额显示：分 → 带货币符号
import { computed } from 'vue'
import type { Money } from '@/api/types'
import { formatMoney } from '@/utils/format'

const props = defineProps<{ money?: Money | null; strong?: boolean }>()
const text = computed(() => formatMoney(props.money))
</script>

<template>
  <span :class="['money', { strong }]">{{ text }}</span>
</template>

<style scoped>
.money {
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}
.strong {
  font-weight: 600;
}
</style>
