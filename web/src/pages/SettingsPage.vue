<script setup lang="ts">
// 设置页：载入 / 保存 / 未保存提示；各分区卡片 v-model 各自的设置片段。密钥字段 "******" 表示已设置且不修改
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { onBeforeRouteLeave } from 'vue-router'
import { NButton, NCard, NCheckbox, NCheckboxGroup, NSpace, NSpin, NTag, NText } from 'naive-ui'
import { egressApi, settingsApi } from '@/api'
import type { EventType, Settings } from '@/api/types'
import { useAction, useAsync, useBusy } from '@/composables/useAsync'
import { eventMeta, notifyResultMeta, type TagMeta } from '@/utils/labels'
import PageHeader from '@/components/PageHeader.vue'
import BarkCard from '@/components/settings/BarkCard.vue'
import EgressCard from '@/components/settings/EgressCard.vue'
import EmailCard from '@/components/settings/EmailCard.vue'
import OrdersCard from '@/components/settings/OrdersCard.vue'
import PollingCard from '@/components/settings/PollingCard.vue'
import SolverCard from '@/components/settings/SolverCard.vue'
import TelegramCard from '@/components/settings/TelegramCard.vue'

const notifyEventOrder: EventType[] = ['restock', 'order', 'challenge', 'account', 'price_change', 'new_product', 'sold_out', 'poll_error']
const notifyOptions = notifyEventOrder.map((item) => ({ value: item, label: eventMeta[item].label }))

const act = useAction()
const { isBusy, track } = useBusy()
const form = ref<Settings | null>(null)
const savedSnapshot = ref('')
const testResults = ref<TagMeta[] | null>(null)

// 表单直接就是请求体：密钥字段由 SecretInput 维持 "******" / 新值 / 空串
const applySettings = (settings: Settings) => {
  form.value = settings
  savedSnapshot.value = JSON.stringify(settings)
}

const { loading, run } = useAsync(async () => {
  const settings = await settingsApi.get()
  applySettings(settings)
  return settings
})

const dirty = computed<boolean>(() => form.value !== null && JSON.stringify(form.value) !== savedSnapshot.value)

const save = async (): Promise<boolean> => {
  const current = form.value
  if (!current) return false
  const saved = await track('save', () => act(() => settingsApi.save(current), '已保存'))
  if (!saved) return false
  applySettings(saved)
  return true
}

const testNotify = async () => {
  if (dirty.value && !(await save())) return
  const result = await track('notify', () => act(() => settingsApi.testNotify()))
  if (result) testResults.value = Object.entries(result.results).map(([channel, value]) => notifyResultMeta(channel, value))
}

// 测试前先保存，保证服务端用的是当前填写的地址
const testEgress = async (id: string) => {
  if (dirty.value && !(await save())) return null
  return act(() => egressApi.test(id))
}

// 未保存离开提示：路由切换 + 刷新 / 关闭标签页
onBeforeRouteLeave(() => {
  if (!dirty.value) return true
  return window.confirm('设置尚未保存，确定离开？')
})
const beforeUnload = (event: BeforeUnloadEvent) => {
  if (!dirty.value) return
  event.preventDefault()
}
window.addEventListener('beforeunload', beforeUnload)
onBeforeUnmount(() => window.removeEventListener('beforeunload', beforeUnload))

onMounted(() => {
  void run()
})
</script>

<template>
  <div class="page">
    <PageHeader title="设置" subtitle="通知渠道、验证码求解、出口、轮询策略与通知事件">
      <n-tag v-if="dirty" type="warning" size="small" :bordered="false" style="align-self: center">未保存</n-tag>
      <n-button type="primary" :loading="isBusy('save')" :disabled="!dirty" @click="save">保存</n-button>
    </PageHeader>
    <n-spin :show="loading && !form">
      <div v-if="form" class="cards">
        <TelegramCard v-model="form.telegram" />
        <EmailCard v-model="form.email" />
        <BarkCard v-model="form.bark" />
        <SolverCard v-model="form.solver" />
        <EgressCard v-model="form.egress" :test="testEgress" />
        <PollingCard v-model="form.polling" />
        <OrdersCard v-model="form.orders" />

        <n-card title="通知事件" size="small">
          <n-checkbox-group v-model:value="form.notify_events">
            <n-space item-style="display: flex">
              <n-checkbox v-for="option in notifyOptions" :key="option.value" :value="option.value" :label="option.label" />
            </n-space>
          </n-checkbox-group>
          <div class="test">
            <n-button :loading="isBusy('notify')" @click="testNotify">发送测试通知</n-button>
            <n-text v-if="dirty" depth="3" style="font-size: 12px">将先保存当前修改</n-text>
          </div>
          <n-space v-if="testResults" :size="8" style="margin-top: 10px">
            <n-tag v-for="item in testResults" :key="item.label" :type="item.type" size="small" :bordered="false">{{ item.label }}</n-tag>
          </n-space>
        </n-card>
      </div>
      <div v-else class="placeholder" />
    </n-spin>
  </div>
</template>

<style scoped>
.cards {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(min(420px, 100%), 1fr));
  gap: 14px;
  align-items: start;
}
.test {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
  margin-top: 16px;
}
.placeholder {
  min-height: 200px;
}
</style>
