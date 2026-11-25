<script setup lang="ts">
// 商品补货记录：每段在售期的补货时间、在售时长与当时价格（最新在前），顶部是近 30 天统计
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { NEmpty, NSpin, NTag, NText } from 'naive-ui'
import { productsApi } from '@/api'
import type { Product, StockWindow } from '@/api/types'
import { useAction } from '@/composables/useAsync'
import { formatDateTime, timeAgo } from '@/utils/format'
import { durationText, formatSpan, windowStats } from '@/utils/stockHistory'

const props = defineProps<{ product: Product }>()
const act = useAction()
const windows = ref<StockWindow[]>([])
const loading = ref(false)
const now = ref(Date.now())
const timer = setInterval(() => {
  now.value = Date.now()
}, 15000)
onBeforeUnmount(() => clearInterval(timer))

watch(
  () => [props.product.site_id, props.product.id, props.product.fetched_at],
  async () => {
    const product = props.product
    loading.value = true
    const list = await act(() => productsApi.stockHistory(product.site_id, product.id))
    if (props.product !== product) return
    loading.value = false
    windows.value = list ?? []
  },
  { immediate: true },
)

const stats = computed(() => {
  const since = now.value - 30 * 86400_000
  return windowStats(windows.value.filter((window) => new Date(window.started_at).getTime() >= since), now.value)
})
</script>

<template>
  <div class="stock-history">
    <div class="head">
      <n-text strong>补货记录</n-text>
      <n-text v-if="stats.count > 0" depth="3">近 30 天 {{ stats.count }} 次 · 平均在售 {{ formatSpan(stats.averageMs) }}</n-text>
    </div>
    <n-spin :show="loading" size="small">
      <n-empty v-if="!loading && windows.length === 0" size="small" description="还没有观测到补货" />
      <div v-for="window in windows" :key="window.id" class="row">
        <span class="when" :title="formatDateTime(window.started_at)">
          {{ formatDateTime(window.started_at).slice(5, 16) }}
          <n-text depth="3">（{{ timeAgo(window.started_at, now) }}）</n-text>
        </span>
        <span class="duration">
          <n-tag v-if="!window.ended_at" size="tiny" type="success" :bordered="false">在售中</n-tag>
          <span :title="window.ended_at ? `售罄于 ${formatDateTime(window.ended_at)}，最后看到有货 ${formatDateTime(window.last_seen_at)}` : ''">
            {{ durationText(window, now) }}
          </span>
        </span>
        <n-text depth="3" class="price">{{ window.price }}<template v-if="window.qty != null"> · {{ window.qty }} 台</template></n-text>
      </div>
    </n-spin>
    <n-text v-if="windows.length > 0" depth="3" class="note">按轮询观测，补货与售罄时间误差约一个轮询间隔；「至少」表示开始记录时已在售。</n-text>
  </div>
</template>

<style scoped>
.head {
  display: flex;
  justify-content: space-between;
  align-items: baseline;
  gap: 8px;
  margin-bottom: 6px;
}
.row {
  display: grid;
  grid-template-columns: minmax(0, 1.3fr) minmax(0, 1fr) minmax(0, 1fr);
  gap: 8px;
  padding: 4px 0;
  font-size: 13px;
  border-bottom: 1px solid var(--n-border-color, rgba(128, 128, 128, 0.15));
}
.duration {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
.price {
  text-align: right;
  font-size: 12px;
}
.note {
  display: block;
  margin-top: 6px;
  font-size: 12px;
}
</style>
