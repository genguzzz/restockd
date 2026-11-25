<script setup lang="ts">
// 商品补货概况一行：在售中「已在售 X」，否则「上次补货 X前 · 在售 Y」；每 15 秒刷新
import { computed, onBeforeUnmount, ref } from 'vue'
import { NText } from 'naive-ui'
import type { StockHistory } from '@/api/types'
import { formatDateTime } from '@/utils/format'
import { stockHistorySummary } from '@/utils/stockHistory'

const props = defineProps<{ history?: StockHistory }>()
const now = ref(Date.now())
const timer = setInterval(() => {
  now.value = Date.now()
}, 15000)
onBeforeUnmount(() => clearInterval(timer))
const text = computed(() => stockHistorySummary(props.history, now.value))
const title = computed(() => {
  const window = props.history?.current ?? props.history?.last
  return window ? `补货于 ${formatDateTime(window.started_at)}（按轮询观测）` : ''
})
</script>

<template>
  <n-text v-if="text" depth="3" class="stock-history" :title="title">{{ text }}</n-text>
</template>

<style scoped>
.stock-history {
  font-size: 12px;
}
</style>
