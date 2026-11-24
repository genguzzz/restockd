<script setup lang="ts">
// 关注 / 自动锁单规则编辑抽屉：新建（可由商品预填）或编辑已有关注，实时估算价格
import { computed, ref, watch as watchSource } from 'vue'
import {
  NAlert,
  NButton,
  NDivider,
  NDrawer,
  NDrawerContent,
  NForm,
  NFormItem,
  NInput,
  NInputNumber,
  NSelect,
  NSpace,
  NSwitch,
  NText,
  NTooltip,
  useMessage,
  type SelectOption,
} from 'naive-ui'
import { accountsApi, productsApi, sitesApi, watchesApi } from '@/api'
import type { Account, BillingCycle, Money, Product, Site, Watch, WatchInput } from '@/api/types'
import { centsToYuan, cycleLabel, formatMoney, stockLabels, yuanToCents } from '@/utils/format'

const props = defineProps<{ show: boolean; watch?: Watch | null; product?: Product | null }>()
const emit = defineEmits<{
  (e: 'update:show', value: boolean): void
  (e: 'saved', watch: Watch): void
}>()

const cycleMonths: Record<BillingCycle, number> = {
  monthly: 1,
  quarterly: 3,
  semiannually: 6,
  annually: 12,
  biennially: 24,
  triennially: 36,
  onetime: 1,
}

interface FormState {
  siteId: string | null
  productId: string | null
  cycle: BillingCycle | null
  options: Record<string, string>
  os: string | null
  hostname: string
  coupon: string
  gateway: string
  qty: number
  accountId: string | null
  autoOrder: boolean
  maxPriceYuan: number | null
  maxOrders: number
  notify: boolean
  enabled: boolean
}

const emptyForm = (): FormState => ({
  siteId: null,
  productId: null,
  cycle: null,
  options: {},
  os: null,
  hostname: '',
  coupon: '',
  gateway: '',
  qty: 1,
  accountId: null,
  autoOrder: false,
  maxPriceYuan: null,
  maxOrders: 1,
  notify: true,
  enabled: true,
})

const message = useMessage()
const form = ref<FormState>(emptyForm())
const sites = ref<Site[]>([])
const products = ref<Product[]>([])
const accounts = ref<Account[]>([])
const loadingProducts = ref(false)
const saving = ref(false)
const isEdit = computed(() => Boolean(props.watch?.id))

const selectedProduct = computed<Product | null>(
  () => products.value.find((item) => item.id === form.value.productId) ?? null,
)
const selectedPrice = computed(() => selectedProduct.value?.prices.find((item) => item.cycle === form.value.cycle) ?? null)
const selectedAccount = computed(() => accounts.value.find((item) => item.id === form.value.accountId) ?? null)
const maxQty = computed(() => {
  const qty = selectedProduct.value?.stock.qty
  return qty !== undefined && qty !== null && qty > 0 ? qty : undefined
})

const siteOptions = computed<SelectOption[]>(() => sites.value.map((site) => ({ label: site.name, value: site.id })))
const productOptions = computed<SelectOption[]>(() =>
  products.value.map((item) => {
    const stock = stockLabels[item.stock.status] ?? item.stock.status
    const qty = item.stock.qty !== undefined && item.stock.status !== 'out_of_stock' ? ` ${item.stock.qty}` : ''
    return { label: `${item.name}（${stock}${qty}）`, value: item.id }
  }),
)
const cycleOptions = computed<SelectOption[]>(() =>
  (selectedProduct.value?.prices ?? []).map((price) => ({
    label: `${cycleLabel(price.cycle)} · ${formatMoney(price.amount)}`,
    value: price.cycle,
  })),
)
const osOptions = computed<SelectOption[]>(() =>
  (selectedProduct.value?.os_templates ?? []).map((item) => ({ label: item.label, value: item.value })),
)
const accountOptions = computed<SelectOption[]>(() =>
  accounts.value.map((item) => ({ label: `${item.label}${item.identity ? `（${item.identity}）` : ''}`, value: item.id })),
)

const choiceLabel = (label: string, delta: Money): string =>
  delta.cents > 0 ? `${label}（+${formatMoney(delta)}/月）` : label
const optionSelectOptions = (key: string): SelectOption[] => {
  const option = selectedProduct.value?.options?.find((item) => item.key === key)
  return (option?.choices ?? []).map((choice) => ({ label: choiceLabel(choice.label, choice.delta_monthly), value: choice.value }))
}

// 价格估算：周期价 × 数量 + 选项月差价 × 周期月数 × 数量
const estimate = computed<{ base: Money; extra: Money; total: Money } | null>(() => {
  const price = selectedPrice.value
  if (!price || !form.value.cycle) return null
  const currency = price.amount.currency
  const months = cycleMonths[form.value.cycle]
  const qty = form.value.qty > 0 ? form.value.qty : 1
  let deltaMonthly = 0
  for (const option of selectedProduct.value?.options ?? []) {
    const value = form.value.options[option.key]
    const choice = option.choices.find((item) => item.value === value)
    if (choice) deltaMonthly += choice.delta_monthly.cents
  }
  const base = price.amount.cents * qty
  const extra = deltaMonthly * months * qty
  return {
    base: { cents: base, currency },
    extra: { cents: extra, currency },
    total: { cents: base + extra + price.setup.cents * qty, currency },
  }
})

const gatewayPlaceholder = computed(() => {
  const gateway = selectedAccount.value?.defaults.gateway
  return gateway ? `留空使用账号默认：${gateway}` : '支付方式，如 alipay'
})
const hostnamePlaceholder = computed(() => selectedAccount.value?.defaults.hostname || '可选')

async function loadSites(): Promise<void> {
  try {
    sites.value = await sitesApi.list()
  } catch (err) {
    message.error(err instanceof Error ? err.message : String(err))
  }
}

async function loadSiteData(siteId: string): Promise<void> {
  loadingProducts.value = true
  try {
    const [productList, accountList] = await Promise.all([productsApi.list({ site_id: siteId }), accountsApi.list(siteId)])
    if (form.value.siteId !== siteId) return
    products.value = productList
    accounts.value = accountList
  } catch (err) {
    message.error(err instanceof Error ? err.message : String(err))
  } finally {
    loadingProducts.value = false
  }
}

// 按商品补齐默认周期与选项（仅填空缺项，不覆盖已有值）
function applyProductDefaults(product: Product): void {
  if (!form.value.cycle || !product.prices.some((item) => item.cycle === form.value.cycle)) {
    form.value.cycle = product.prices[0]?.cycle ?? null
  }
  const options: Record<string, string> = {}
  for (const option of product.options ?? []) {
    const current = form.value.options[option.key]
    const valid = option.choices.some((choice) => choice.value === current)
    const fallback = option.choices[0]?.value
    if (valid && current !== undefined) options[option.key] = current
    else if (fallback !== undefined) options[option.key] = fallback
  }
  form.value.options = options
  if (form.value.os && !(product.os_templates ?? []).some((item) => item.value === form.value.os)) form.value.os = null
}

function fromWatch(source: Watch): FormState {
  return {
    siteId: source.site_id,
    productId: source.product_id,
    cycle: source.cycle,
    options: { ...(source.options ?? {}) },
    os: source.os || null,
    hostname: source.hostname ?? '',
    coupon: source.coupon ?? '',
    gateway: source.gateway ?? '',
    qty: source.qty > 0 ? source.qty : 1,
    accountId: source.account_id || null,
    autoOrder: source.auto_order,
    maxPriceYuan: centsToYuan(source.max_price_cents),
    maxOrders: source.max_orders,
    notify: source.notify,
    enabled: source.enabled,
  }
}

async function initialize(): Promise<void> {
  products.value = []
  accounts.value = []
  if (props.watch) form.value = fromWatch(props.watch)
  else {
    form.value = emptyForm()
    if (props.product) {
      form.value.siteId = props.product.site_id
      form.value.productId = props.product.id
    }
  }
  void loadSites()
  const siteId = form.value.siteId
  if (!siteId) return
  await loadSiteData(siteId)
  const product = selectedProduct.value
  if (product && !props.watch) applyProductDefaults(product)
  if (!form.value.accountId && accounts.value.length === 1 && !props.watch) form.value.accountId = accounts.value[0]?.id ?? null
}

watchSource(
  () => props.show,
  (visible) => {
    if (visible) void initialize()
  },
  { immediate: true },
)

function onSiteChange(siteId: string | null): void {
  if (siteId === form.value.siteId) return
  form.value.siteId = siteId
  form.value.productId = null
  form.value.cycle = null
  form.value.options = {}
  form.value.os = null
  form.value.accountId = null
  form.value.autoOrder = false
  products.value = []
  accounts.value = []
  if (siteId) void loadSiteData(siteId)
}

function onProductChange(productId: string | null): void {
  form.value.productId = productId
  const product = selectedProduct.value
  if (product) applyProductDefaults(product)
  else {
    form.value.cycle = null
    form.value.options = {}
  }
}

function onAccountChange(accountId: string | null): void {
  form.value.accountId = accountId
  if (!accountId) form.value.autoOrder = false
}

function setOption(key: string, value: string): void {
  form.value.options = { ...form.value.options, [key]: value }
}

function validate(): string | null {
  if (!form.value.siteId) return '请选择站点'
  if (!form.value.productId) return '请选择商品'
  if (!form.value.cycle) return '请选择计费周期'
  if (form.value.autoOrder && !form.value.accountId) return '自动锁单需要选择账号'
  return null
}

function buildInput(): WatchInput {
  const state = form.value
  return {
    site_id: state.siteId ?? '',
    product_id: state.productId ?? '',
    cycle: state.cycle ?? 'monthly',
    options: state.options,
    os: state.os ?? '',
    hostname: state.hostname.trim(),
    coupon: state.coupon.trim(),
    gateway: state.gateway.trim(),
    qty: state.qty > 0 ? state.qty : 1,
    account_id: state.accountId ?? '',
    auto_order: state.autoOrder && Boolean(state.accountId),
    max_price_cents: yuanToCents(state.maxPriceYuan),
    max_orders: state.maxOrders >= 0 ? state.maxOrders : 0,
    notify: state.notify,
    enabled: state.enabled,
  }
}

async function submit(): Promise<void> {
  const problem = validate()
  if (problem) {
    message.warning(problem)
    return
  }
  saving.value = true
  try {
    const input = buildInput()
    const saved = props.watch?.id ? await watchesApi.update(props.watch.id, input) : await watchesApi.create(input)
    message.success(isEdit.value ? '关注已更新' : '已添加关注')
    emit('saved', saved)
    emit('update:show', false)
  } catch (err) {
    message.error(err instanceof Error ? err.message : String(err))
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <n-drawer
    :show="show"
    :width="'min(520px, 100vw)'"
    placement="right"
    @update:show="(value: boolean) => emit('update:show', value)"
  >
    <n-drawer-content :title="isEdit ? '编辑关注' : '新建关注'" closable :native-scrollbar="false">
      <n-form label-placement="top">
        <n-form-item label="站点" required>
          <n-select
            :value="form.siteId"
            :options="siteOptions"
            placeholder="选择站点"
            filterable
            :disabled="isEdit"
            @update:value="onSiteChange"
          />
        </n-form-item>
        <n-form-item label="商品" required>
          <n-select
            :value="form.productId"
            :options="productOptions"
            :loading="loadingProducts"
            :disabled="!form.siteId"
            placeholder="选择商品"
            filterable
            @update:value="onProductChange"
          />
        </n-form-item>
        <n-form-item label="计费周期" required>
          <n-select v-model:value="form.cycle" :options="cycleOptions" :disabled="!selectedProduct" placeholder="选择周期" />
        </n-form-item>
        <n-form-item v-for="option in selectedProduct?.options ?? []" :key="option.key" :label="option.label">
          <n-select
            :value="form.options[option.key] ?? null"
            :options="optionSelectOptions(option.key)"
            @update:value="(value: string) => setOption(option.key, value)"
          />
        </n-form-item>
        <n-form-item v-if="osOptions.length" label="操作系统">
          <n-select v-model:value="form.os" :options="osOptions" clearable placeholder="留空使用账号默认" />
        </n-form-item>
        <n-form-item label="主机名">
          <n-input v-model:value="form.hostname" :placeholder="hostnamePlaceholder" />
        </n-form-item>
        <n-form-item label="优惠码">
          <n-input v-model:value="form.coupon" placeholder="可选" />
        </n-form-item>
        <n-form-item label="支付方式">
          <n-input v-model:value="form.gateway" :placeholder="gatewayPlaceholder" />
        </n-form-item>
        <n-form-item label="数量">
          <n-input-number v-model:value="form.qty" :min="1" :max="maxQty" :precision="0" style="width: 100%" />
        </n-form-item>

        <n-divider title-placement="left">自动锁单</n-divider>
        <n-form-item label="下单账号" :required="form.autoOrder">
          <n-select
            :value="form.accountId"
            :options="accountOptions"
            :disabled="!form.siteId"
            clearable
            placeholder="选择该站点的托管账号"
            @update:value="onAccountChange"
          />
        </n-form-item>
        <n-form-item label="补货时自动锁单">
          <n-tooltip :disabled="Boolean(form.accountId)">
            <template #trigger>
              <n-switch v-model:value="form.autoOrder" :disabled="!form.accountId" />
            </template>
            请先选择下单账号
          </n-tooltip>
        </n-form-item>
        <n-form-item label="价格上限（元，0 / 留空为不限）">
          <n-space vertical :size="4" style="width: 100%">
            <n-input-number v-model:value="form.maxPriceYuan" :min="0" :precision="2" clearable placeholder="不限" style="width: 100%" />
            <n-text v-if="estimate" depth="3" style="font-size: 12px">当前价 {{ formatMoney(estimate.total) }}</n-text>
          </n-space>
        </n-form-item>
        <n-form-item label="自动锁单次数上限（0 为不限）">
          <n-input-number v-model:value="form.maxOrders" :min="0" :precision="0" style="width: 100%" />
        </n-form-item>
        <n-space :size="24">
          <n-form-item label="通知">
            <n-switch v-model:value="form.notify" />
          </n-form-item>
          <n-form-item label="启用">
            <n-switch v-model:value="form.enabled" />
          </n-form-item>
        </n-space>

        <n-alert v-if="estimate" type="info" :show-icon="false" title="预估价格">
          <div>{{ cycleLabel(form.cycle) }} {{ formatMoney(selectedPrice?.amount) }} × {{ form.qty }} = {{ formatMoney(estimate.base) }}</div>
          <div v-if="estimate.extra.cents > 0">选项加价 {{ formatMoney(estimate.extra) }}</div>
          <div v-if="selectedPrice && selectedPrice.setup.cents > 0">初装费 {{ formatMoney(selectedPrice.setup) }} × {{ form.qty }}</div>
          <div>
            合计约 <strong>{{ formatMoney(estimate.total) }}</strong>
            <n-text depth="3" style="font-size: 12px">（未计优惠码）</n-text>
          </div>
        </n-alert>
      </n-form>
      <template #footer>
        <n-space justify="end">
          <n-button @click="emit('update:show', false)">取消</n-button>
          <n-button type="primary" :loading="saving" @click="submit">保存</n-button>
        </n-space>
      </template>
    </n-drawer-content>
  </n-drawer>
</template>
