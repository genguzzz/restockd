<script setup lang="ts">
// 出口列表的一行：名称 / 地址 / 启用 / 测试 / 删除。内置直连出口只读（不可改地址、不可删除）。
import { NButton, NInput, NSpace, NSwitch, NTag, NText } from 'naive-ui'
import type { EgressProxy, EgressTestResult } from '@/api/types'

const props = defineProps<{
  egress: EgressProxy
  builtin?: boolean
  testing?: boolean
  result?: EgressTestResult
}>()
const emit = defineEmits<{
  (e: 'update', value: EgressProxy): void
  (e: 'test'): void
  (e: 'remove'): void
}>()

const patch = (fields: Partial<EgressProxy>) => emit('update', { ...props.egress, ...fields })
const resultText = (result: EgressTestResult): string =>
  result.ok ? `${result.ip ?? '?'} · ${result.loc ?? '?'} · ${result.ms ?? '?'}ms` : (result.error ?? '失败')
</script>

<template>
  <div class="row">
    <n-space :size="6" align="center" :wrap="false">
      <n-tag v-if="builtin" size="small" :bordered="false" type="info">内置</n-tag>
      <n-input
        :value="egress.name"
        size="small"
        :disabled="builtin"
        placeholder="名称，如 dmit LA"
        @update:value="(value: string) => patch({ name: value })"
      />
      <n-switch v-if="!builtin" :value="egress.enabled" size="small" @update:value="(value: boolean) => patch({ enabled: value })" />
      <n-button size="tiny" :loading="testing" @click="emit('test')">测试</n-button>
      <n-button v-if="!builtin" size="tiny" type="error" secondary @click="emit('remove')">删除</n-button>
    </n-space>
    <n-input
      v-if="!builtin"
      :value="egress.url"
      size="small"
      placeholder="socks5://127.0.0.1:1080 或 http://user:pass@host:port"
      @update:value="(value: string) => patch({ url: value })"
    />
    <n-text v-else depth="3" class="hint">本服务器自身的出口 IP</n-text>
    <n-text v-if="result" :type="result.ok ? 'success' : 'error'" class="hint">{{ resultText(result) }}</n-text>
  </div>
</template>

<style scoped>
.row {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding-bottom: 10px;
  border-bottom: 1px dashed rgba(128, 128, 128, 0.25);
}
.hint {
  font-size: 12px;
}
</style>
