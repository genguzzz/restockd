<script setup lang="ts">
// 下单规格表单（关注规则与商品下单共用）：计费周期（列出全部价格）、配置选项、账号、OS、主机名、优惠码、支付方式、数量与预估价
import { computed } from 'vue'
import { NAlert, NFormItem, NInput, NInputNumber, NRadioButton, NRadioGroup, NSelect, NText, type SelectOption } from 'naive-ui'
import type { Account, Money, Product } from '@/api/types'
import { cycleLabel, formatMoney } from '@/utils/format'
import { estimatePrice, type OrderSpecState } from '@/utils/orderSpec'

const props = defineProps<{ product: Product | null; accounts: Account[]; accountRequired?: boolean }>()
const model = defineModel<OrderSpecState>({ required: true })

const account = computed(() => props.accounts.find((item) => item.id === model.value.accountId) ?? null)
const estimate = computed(() => estimatePrice(props.product, model.value.cycle, model.value.options, model.value.qty))
const maxQty = computed(() => {
  const qty = props.product?.stock.qty
  return qty !== undefined && qty > 0 ? qty : undefined
})
const accountOptions = computed<SelectOption[]>(() =>
  props.accounts.map((item) => ({ label: `${item.label}${item.identity ? `（${item.identity}）` : ''}`, value: item.id })),
)
const osOptions = computed<SelectOption[]>(() => (props.product?.os_templates ?? []).map((item) => ({ label: item.label, value: item.value })))
const choiceLabel = (label: string, delta: Money): string => (delta.cents > 0 ? `${label}（+${formatMoney(delta)}/月）` : label)
const choiceOptions = (key: string): SelectOption[] =>
  (props.product?.options?.find((item) => item.key === key)?.choices ?? []).map((choice) => ({
    label: choiceLabel(choice.label, choice.delta_monthly),
    value: choice.value,
  }))
const setOption = (key: string, value: string) => {
  model.value.options = { ...model.value.options, [key]: value }
}
const defaultHint = (value: string | undefined, fallback: string) => (value ? `留空使用账号默认：${value}` : fallback)
</script>

<template>
  <n-form-item label="计费周期" required>
    <n-radio-group v-if="product?.prices.length" v-model:value="model.cycle" size="small" class="cycles">
      <n-radio-button v-for="price in product.prices" :key="price.cycle" :value="price.cycle">
        {{ cycleLabel(price.cycle) }} {{ formatMoney(price.amount) }}
      </n-radio-button>
    </n-radio-group>
    <n-text v-else depth="3">{{ product ? '暂无价格' : '请先选择商品' }}</n-text>
  </n-form-item>
  <n-form-item v-for="option in product?.options ?? []" :key="option.key" :label="option.label">
    <n-select :value="model.options[option.key] ?? null" :options="choiceOptions(option.key)" @update:value="(value: string) => setOption(option.key, value)" />
  </n-form-item>
  <n-form-item label="下单账号" :required="accountRequired">
    <n-select v-model:value="model.accountId" :options="accountOptions" clearable placeholder="选择该站点的托管账号" />
  </n-form-item>
  <n-form-item v-if="osOptions.length" label="操作系统">
    <n-select v-model:value="model.os" :options="osOptions" clearable :placeholder="defaultHint(account?.defaults.os, '留空使用账号默认')" />
  </n-form-item>
  <n-form-item label="主机名">
    <n-input v-model:value="model.hostname" :placeholder="defaultHint(account?.defaults.hostname, '可选')" />
  </n-form-item>
  <n-form-item label="优惠码">
    <n-input v-model:value="model.coupon" :placeholder="defaultHint(account?.defaults.coupon, '可选')" />
  </n-form-item>
  <n-form-item label="支付方式">
    <n-input v-model:value="model.gateway" :placeholder="defaultHint(account?.defaults.gateway, '支付方式，如 alipay')" />
  </n-form-item>
  <n-form-item label="数量">
    <n-input-number v-model:value="model.qty" :min="1" :max="maxQty" :precision="0" style="width: 100%" />
  </n-form-item>
  <n-alert v-if="estimate" type="info" :show-icon="false" title="预估价格" class="estimate">
    <div>{{ cycleLabel(estimate.price.cycle) }} {{ formatMoney(estimate.price.amount) }} × {{ estimate.qty }} = {{ formatMoney(estimate.base) }}</div>
    <div v-if="estimate.extra.cents > 0">选项加价 {{ formatMoney(estimate.extra) }}</div>
    <div v-if="estimate.price.setup.cents > 0">初装费 {{ formatMoney(estimate.price.setup) }} × {{ estimate.qty }}</div>
    <div>
      合计约 <strong>{{ formatMoney(estimate.total) }}</strong>
      <n-text depth="3" style="font-size: 12px">（未计优惠码）</n-text>
    </div>
  </n-alert>
</template>

<style scoped>
.cycles {
  display: flex;
  flex-wrap: wrap;
  row-gap: 6px;
}
.estimate {
  margin-bottom: 16px;
}
</style>
