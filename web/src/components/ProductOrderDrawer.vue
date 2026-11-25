<script setup lang="ts">
// 商品详情与直接下单：选周期 / 配置 / 账号等，试下单或锁单（真实锁单前确认，有效期内重复锁单再次确认）
import { computed, ref, watch } from 'vue'
import { NAlert, NButton, NDrawer, NDrawerContent, NForm, NSpace, NText, useMessage } from 'naive-ui'
import { accountsApi, productsApi } from '@/api'
import type { Account, Order, Product, ProductOrderInput } from '@/api/types'
import { useAction, useBusy } from '@/composables/useAsync'
import { priorOfProduct, useOrderPlacer } from '@/composables/useOrderPlacer'
import { useSites } from '@/composables/useSites'
import { cycleLabel, formatMoney } from '@/utils/format'
import { applyProductDefaults, defaultAccount, emptySpec, estimatePrice, type OrderSpecState } from '@/utils/orderSpec'
import LastOrderTag from './LastOrderTag.vue'
import OrderResultModal from './OrderResultModal.vue'
import OrderSpecForm from './OrderSpecForm.vue'
import SpecLine from './SpecLine.vue'
import StockHistoryList from './StockHistoryList.vue'
import StockBadge from './StockBadge.vue'

const props = defineProps<{ show: boolean; product: Product | null }>()
const emit = defineEmits<{
  (e: 'update:show', value: boolean): void
  (e: 'ordered', order: Order): void
  /** 屏蔽该商品（由页面执行并从列表移除），抽屉随即关闭 */
  (e: 'hide', product: Product): void
}>()

const message = useMessage()
const act = useAction()
const place = useOrderPlacer()
const { isBusy, track } = useBusy()
const { siteNames } = useSites()
const accounts = ref<Account[]>([])
const spec = ref<OrderSpecState>(emptySpec())
const result = ref<Order | null>(null)
const estimate = computed(() => estimatePrice(props.product, spec.value.cycle, spec.value.options, spec.value.qty))

watch(
  () => props.show,
  async (visible) => {
    const product = props.product
    if (!visible || !product) return
    accounts.value = []
    spec.value = applyProductDefaults(emptySpec(), product)
    const list = await track('accounts', () => act(() => accountsApi.list(product.site_id)))
    if (!list || props.product !== product) return
    accounts.value = list
    spec.value.accountId = defaultAccount(list)?.id ?? null
  },
)

// 选账号时用其默认下单参数预填空着的 OS / 主机名 / 支付方式
watch(
  () => spec.value.accountId,
  (accountId) => {
    const defaults = accounts.value.find((item) => item.id === accountId)?.defaults
    if (!defaults) return
    const current = spec.value
    const osValid = (props.product?.os_templates ?? []).some((item) => item.value === defaults.os)
    if (!current.os && defaults.os && osValid) current.os = defaults.os
    if (!current.hostname && defaults.hostname) current.hostname = defaults.hostname
    if (!current.gateway && defaults.gateway) current.gateway = defaults.gateway
  },
)

const buildInput = (dryRun: boolean): ProductOrderInput | null => {
  const current = spec.value
  if (!current.cycle) {
    message.warning('请选择计费周期')
    return null
  }
  if (!current.accountId) {
    message.warning('请选择下单账号')
    return null
  }
  return {
    account_id: current.accountId,
    cycle: current.cycle,
    options: current.options,
    os: current.os ?? '',
    hostname: current.hostname.trim(),
    coupon: current.coupon.trim(),
    gateway: current.gateway.trim(),
    qty: current.qty > 0 ? current.qty : 1,
    dry_run: dryRun,
  }
}

const hideProduct = (product: Product) => {
  emit('hide', product)
  emit('update:show', false)
}

const submit = async (dryRun: boolean) => {
  const product = props.product
  const input = buildInput(dryRun)
  if (!product || !input) return
  const summary = `${cycleLabel(input.cycle)} × ${input.qty}，预估 ${formatMoney(estimate.value?.total)}（未计优惠码）`
  const order = await track(dryRun ? 'dry' : 'order', () =>
    place((force) => productsApi.order(product.site_id, product.id, force ? { ...input, force } : input), {
      dryRun,
      confirm: `将用所选账号在站点创建一笔真实的未支付订单（需在站点内手动付款）：${summary}。`,
      prior: priorOfProduct(product),
    }),
  )
  if (!order) return
  result.value = order
  emit('ordered', order)
}
</script>

<template>
  <n-drawer :show="show" width="min(520px, 100vw)" @update:show="(value: boolean) => emit('update:show', value)">
    <n-drawer-content v-if="product" :title="product.name" closable :native-scrollbar="false">
      <n-space vertical :size="8" class="head">
        <n-space :size="8" align="center">
          <StockBadge :stock="product.stock" />
          <n-text depth="3">{{ siteNames[product.site_id] ?? product.site_id }}</n-text>
          <a v-if="product.url" :href="product.url" target="_blank" rel="noopener noreferrer">商品页</a>
          <n-button v-if="!product.hidden" text size="tiny" @click="hideProduct(product)">屏蔽</n-button>
        </n-space>
        <SpecLine :specs="product.specs" />
        <n-alert v-if="product.last_order" :type="product.last_order.status === 'placed' ? 'warning' : 'default'" :show-icon="false">
          <n-space :size="6" align="center">
            <n-text>最近一次下单：</n-text>
            <LastOrderTag :order="product.last_order" />
          </n-space>
          <n-text v-if="product.last_order.error" type="error" class="error">{{ product.last_order.error }}</n-text>
        </n-alert>
      </n-space>
      <n-form label-placement="top">
        <OrderSpecForm v-model="spec" :product="product" :accounts="accounts" account-required />
      </n-form>
      <StockHistoryList :product="product" class="history" />
      <template #footer>
        <n-space justify="end">
          <n-button secondary type="info" :loading="isBusy('dry')" :disabled="isBusy('order')" @click="submit(true)">试下单</n-button>
          <n-button type="warning" :loading="isBusy('order')" :disabled="isBusy('dry')" @click="submit(false)">锁单</n-button>
        </n-space>
      </template>
    </n-drawer-content>
  </n-drawer>
  <OrderResultModal :order="result" @close="result = null" @cancelled="(value) => (result = value)" />
</template>

<style scoped>
.head {
  margin-bottom: 16px;
}
.history {
  margin-top: 8px;
}
.error {
  display: block;
  font-size: 12px;
  word-break: break-all;
  margin-top: 4px;
}
</style>
