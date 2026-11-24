<script setup lang="ts">
// 取消订单按钮：仅已锁单、且站点支持取消时显示；确认后调用后端，站点不允许取消时由 useAction 提示原因
import { computed, ref } from 'vue'
import { NButton, NPopconfirm } from 'naive-ui'
import { ordersApi } from '@/api'
import type { Order } from '@/api/types'
import { useAction } from '@/composables/useAsync'
import { useProviders, useSites } from '@/composables/useSites'

const props = defineProps<{ order: Order; size?: 'tiny' | 'small' | 'medium' }>()
const emit = defineEmits<{ (e: 'cancelled', order: Order): void }>()

const act = useAction()
const { sites } = useSites()
const { providerOf } = useProviders()
const busy = ref(false)

const supported = computed(() => {
  const site = sites.value.find((item) => item.id === props.order.site_id)
  return Boolean(site && providerOf(site.kind)?.capabilities.cancel)
})

const cancel = async () => {
  busy.value = true
  const result = await act(() => ordersApi.cancel(props.order.id), '订单已取消')
  busy.value = false
  if (result) emit('cancelled', result)
}
</script>

<template>
  <n-popconfirm v-if="order.status === 'placed' && supported" @positive-click="cancel">
    <template #trigger>
      <n-button :size="size ?? 'tiny'" :loading="busy" type="error" secondary @click.stop>取消订单</n-button>
    </template>
    在站点取消这笔未付款订单（账单作废）？部分站点不允许客户自助取消，会提示原因。
  </n-popconfirm>
</template>
