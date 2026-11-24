<script setup lang="ts">
// 关注 / 自动锁单规则编辑抽屉：新建（可由商品预填）或编辑已有关注；下单规格用共享的 OrderSpecForm
import { computed, ref, watch as watchSource } from 'vue'
import {
  NButton,
  NDivider,
  NDrawer,
  NDrawerContent,
  NForm,
  NFormItem,
  NInputNumber,
  NSelect,
  NSpace,
  NSwitch,
  NText,
  NTooltip,
  useMessage,
  type SelectOption,
} from 'naive-ui'
import { accountsApi, productsApi, watchesApi } from '@/api'
import type { Account, Product, Watch, WatchInput } from '@/api/types'
import { useAction, useBusy } from '@/composables/useAsync'
import { useSites } from '@/composables/useSites'
import { centsToYuan, formatMoney, yuanToCents } from '@/utils/format'
import { metaOf, stockMeta } from '@/utils/labels'
import { applyProductDefaults, emptySpec, estimatePrice, type OrderSpecState } from '@/utils/orderSpec'
import OrderSpecForm from './OrderSpecForm.vue'

const props = defineProps<{ show: boolean; watch?: Watch | null; product?: Product | null }>()
const emit = defineEmits<{
  (e: 'update:show', value: boolean): void
  (e: 'saved', watch: Watch): void
}>()

interface RuleState {
  siteId: string | null
  productId: string | null
  autoOrder: boolean
  maxPriceYuan: number | null
  maxOrders: number
  notify: boolean
  enabled: boolean
}

const emptyRule = (): RuleState => ({ siteId: null, productId: null, autoOrder: false, maxPriceYuan: null, maxOrders: 1, notify: true, enabled: true })

const message = useMessage()
const act = useAction()
const { isBusy, track } = useBusy()
const { siteOptions } = useSites()
const rule = ref<RuleState>(emptyRule())
const spec = ref<OrderSpecState>(emptySpec())
const products = ref<Product[]>([])
const accounts = ref<Account[]>([])
const isEdit = computed(() => Boolean(props.watch?.id))

const selectedProduct = computed<Product | null>(() => products.value.find((item) => item.id === rule.value.productId) ?? null)
const estimate = computed(() => estimatePrice(selectedProduct.value, spec.value.cycle, spec.value.options, spec.value.qty))
const productOptions = computed<SelectOption[]>(() =>
  products.value.map((item) => {
    const qty = item.stock.qty !== undefined && item.stock.status !== 'out_of_stock' ? ` ${item.stock.qty}` : ''
    return { label: `${item.name}（${metaOf(stockMeta, item.stock.status).label}${qty}）`, value: item.id }
  }),
)

async function loadSiteData(siteId: string): Promise<void> {
  const result = await track('products', () => act(() => Promise.all([productsApi.list({ site_id: siteId }), accountsApi.list(siteId)])))
  if (!result || rule.value.siteId !== siteId) return
  ;[products.value, accounts.value] = result
}

function fromWatch(source: Watch): void {
  rule.value = {
    siteId: source.site_id,
    productId: source.product_id,
    autoOrder: source.auto_order,
    maxPriceYuan: centsToYuan(source.max_price_cents),
    maxOrders: source.max_orders,
    notify: source.notify,
    enabled: source.enabled,
  }
  spec.value = {
    cycle: source.cycle,
    options: { ...(source.options ?? {}) },
    os: source.os || null,
    hostname: source.hostname ?? '',
    coupon: source.coupon ?? '',
    gateway: source.gateway ?? '',
    qty: source.qty > 0 ? source.qty : 1,
    accountId: source.account_id || null,
  }
}

async function initialize(): Promise<void> {
  products.value = []
  accounts.value = []
  spec.value = emptySpec()
  rule.value = emptyRule()
  if (props.watch) fromWatch(props.watch)
  else if (props.product) {
    rule.value.siteId = props.product.site_id
    rule.value.productId = props.product.id
  }
  const siteId = rule.value.siteId
  if (!siteId) return
  await loadSiteData(siteId)
  if (props.watch) return
  const product = selectedProduct.value
  if (product) spec.value = applyProductDefaults(spec.value, product)
  if (!spec.value.accountId && accounts.value.length === 1) spec.value.accountId = accounts.value[0]?.id ?? null
}

watchSource(
  () => props.show,
  (visible) => {
    if (visible) void initialize()
  },
  { immediate: true },
)

// 没有账号就不能自动锁单
watchSource(
  () => spec.value.accountId,
  (accountId) => {
    if (!accountId) rule.value.autoOrder = false
  },
)

function onSiteChange(siteId: string | null): void {
  if (siteId === rule.value.siteId) return
  rule.value = { ...rule.value, siteId, productId: null, autoOrder: false }
  spec.value = emptySpec()
  products.value = []
  accounts.value = []
  if (siteId) void loadSiteData(siteId)
}

function onProductChange(productId: string | null): void {
  rule.value.productId = productId
  const product = selectedProduct.value
  spec.value = product ? applyProductDefaults(spec.value, product) : { ...spec.value, cycle: null, options: {} }
}

function validate(): string | null {
  if (!rule.value.siteId) return '请选择站点'
  if (!rule.value.productId) return '请选择商品'
  if (!spec.value.cycle) return '请选择计费周期'
  if (rule.value.autoOrder && !spec.value.accountId) return '自动锁单需要选择账号'
  return null
}

function buildInput(): WatchInput {
  const { siteId, productId, autoOrder, maxPriceYuan, maxOrders, notify, enabled } = rule.value
  const current = spec.value
  return {
    site_id: siteId ?? '',
    product_id: productId ?? '',
    cycle: current.cycle ?? 'monthly',
    options: current.options,
    os: current.os ?? '',
    hostname: current.hostname.trim(),
    coupon: current.coupon.trim(),
    gateway: current.gateway.trim(),
    qty: current.qty > 0 ? current.qty : 1,
    account_id: current.accountId ?? '',
    auto_order: autoOrder && Boolean(current.accountId),
    max_price_cents: yuanToCents(maxPriceYuan),
    max_orders: maxOrders >= 0 ? maxOrders : 0,
    notify,
    enabled,
  }
}

async function submit(): Promise<void> {
  const problem = validate()
  if (problem) {
    message.warning(problem)
    return
  }
  const input = buildInput()
  const id = props.watch?.id
  const saved = await track('save', () =>
    act(() => (id ? watchesApi.update(id, input) : watchesApi.create(input)), id ? '关注已更新' : '已添加关注'),
  )
  if (!saved) return
  emit('saved', saved)
  emit('update:show', false)
}
</script>

<template>
  <n-drawer :show="show" :width="'min(520px, 100vw)'" placement="right" @update:show="(value: boolean) => emit('update:show', value)">
    <n-drawer-content :title="isEdit ? '编辑关注' : '新建关注'" closable :native-scrollbar="false">
      <n-form label-placement="top">
        <n-form-item label="站点" required>
          <n-select :value="rule.siteId" :options="siteOptions" placeholder="选择站点" filterable :disabled="isEdit" @update:value="onSiteChange" />
        </n-form-item>
        <n-form-item label="商品" required>
          <n-select
            :value="rule.productId"
            :options="productOptions"
            :loading="isBusy('products')"
            :disabled="!rule.siteId"
            placeholder="选择商品"
            filterable
            @update:value="onProductChange"
          />
        </n-form-item>
        <OrderSpecForm v-model="spec" :product="selectedProduct" :accounts="accounts" :account-required="rule.autoOrder" />

        <n-divider title-placement="left">自动锁单</n-divider>
        <n-form-item label="补货时自动锁单">
          <n-tooltip :disabled="Boolean(spec.accountId)">
            <template #trigger>
              <n-switch v-model:value="rule.autoOrder" :disabled="!spec.accountId" />
            </template>
            请先选择下单账号
          </n-tooltip>
        </n-form-item>
        <n-form-item label="价格上限（元，0 / 留空为不限）">
          <n-space vertical :size="4" style="width: 100%">
            <n-input-number v-model:value="rule.maxPriceYuan" :min="0" :precision="2" clearable placeholder="不限" style="width: 100%" />
            <n-text v-if="estimate" depth="3" style="font-size: 12px">当前价 {{ formatMoney(estimate.total) }}</n-text>
          </n-space>
        </n-form-item>
        <n-form-item label="自动锁单次数上限（0 为不限）">
          <n-input-number v-model:value="rule.maxOrders" :min="0" :precision="0" style="width: 100%" />
        </n-form-item>
        <n-space :size="24">
          <n-form-item label="通知">
            <n-switch v-model:value="rule.notify" />
          </n-form-item>
          <n-form-item label="启用">
            <n-switch v-model:value="rule.enabled" />
          </n-form-item>
        </n-space>
      </n-form>
      <template #footer>
        <n-space justify="end">
          <n-button @click="emit('update:show', false)">取消</n-button>
          <n-button type="primary" :loading="isBusy('save')" @click="submit">保存</n-button>
        </n-space>
      </template>
    </n-drawer-content>
  </n-drawer>
</template>
