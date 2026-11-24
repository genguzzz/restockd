<script setup lang="ts">
// 设置 · 出口：内置直连 + 登记的代理，测试连通性，默认出口优先级
import { computed, ref } from 'vue'
import { NAlert, NButton, NCard, NForm, NFormItem, NSelect, useDialog } from 'naive-ui'
import type { EgressProxy, EgressTestResult, Settings } from '@/api/types'
import EgressRow from '@/components/EgressRow.vue'
import { useBusy } from '@/composables/useAsync'
import { useSites } from '@/composables/useSites'
import { DIRECT_EGRESS_ID, egressOptions, newEgressId } from '@/utils/egress'
import { removeById, upsertById } from '@/utils/list'

const model = defineModel<Settings['egress']>({ required: true })
// 由页面实现：先保存未保存的修改再测试，失败返回 null（错误已提示）
const props = defineProps<{ test: (id: string) => Promise<EgressTestResult | null> }>()

const dialog = useDialog()
const { sites } = useSites()
const { isBusy, track } = useBusy()
const results = ref<Record<string, EgressTestResult>>({})
const options = computed(() => egressOptions(model.value.proxies))

const runTest = async (id: string) => {
  const result = await track(id, () => props.test(id))
  const { [id]: _previous, ...rest } = results.value
  results.value = result ? { ...rest, [id]: result } : rest
}

const add = () => {
  model.value.proxies = [...model.value.proxies, { id: newEgressId(model.value.proxies), name: '', url: '', enabled: true }]
}
const update = (proxy: EgressProxy) => {
  model.value.proxies = upsertById(model.value.proxies, proxy)
}
const doRemove = (id: string) => {
  model.value.proxies = removeById(model.value.proxies, id)
  model.value.default = model.value.default.filter((item) => item !== id)
}
// 仍被站点引用时先确认，列出受影响的站点
const remove = (proxy: EgressProxy) => {
  const users = sites.value.filter((site) => site.egress?.includes(proxy.id)).map((site) => site.name)
  if (users.length === 0) {
    doRemove(proxy.id)
    return
  }
  dialog.warning({
    title: `删除出口「${proxy.name || proxy.id}」？`,
    content: `以下站点的出口优先级里还有它：${users.join('、')}。删除后这些站点将不再使用该出口，建议检查它们的出口设置。`,
    positiveText: '删除',
    negativeText: '取消',
    onPositiveClick: () => doRemove(proxy.id),
  })
}
</script>

<template>
  <n-card title="出口" size="small">
    <n-alert type="default" :show-icon="false" class="card-note">
      站点按优先级使用出口（直连也是一个出口）：连接失败或被站点限流（429）时自动换下一个，并暂时冷却出问题的出口
      （限流只对被限的页面生效）。下单等非幂等请求只在连接未建立时换出口，避免重复提交。站点可在「站点 → 出口优先级」单独指定。
    </n-alert>
    <div class="proxies">
      <EgressRow builtin :testing="isBusy(DIRECT_EGRESS_ID)" :result="results[DIRECT_EGRESS_ID]" @test="runTest(DIRECT_EGRESS_ID)" />
      <EgressRow
        v-for="item in model.proxies"
        :key="item.id"
        :egress="item"
        :testing="isBusy(item.id)"
        :result="results[item.id]"
        @update="update"
        @test="runTest(item.id)"
        @remove="remove(item)"
      />
      <div>
        <n-button size="small" dashed @click="add">添加出口（代理）</n-button>
      </div>
    </div>
    <n-form label-placement="top" size="small" style="margin-top: 12px">
      <n-form-item label="默认出口优先级" feedback="未单独指定出口的站点使用；按选择顺序排优先级，留空即只用直连">
        <n-select v-model:value="model.default" multiple :options="options" placeholder="直连" />
      </n-form-item>
    </n-form>
  </n-card>
</template>

<style scoped>
.proxies {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
</style>
