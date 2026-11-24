<script setup lang="ts">
// 出口列表的一行：名称 / 地址 / 启用 / 测试 / 删除。builtin 时为内置直连出口（只读、只能测试）
import { NButton, NInput, NSpace, NSwitch, NTag, NText } from 'naive-ui'
import type { EgressProxy, EgressTestResult } from '@/api/types'
import { DIRECT_EGRESS_LABEL } from '@/utils/egress'
import { URL_TEXT } from '@/utils/placeholders'

const props = defineProps<{
  egress?: EgressProxy
  builtin?: boolean
  testing?: boolean
  result?: EgressTestResult
}>()
const emit = defineEmits<{
  (e: 'update', value: EgressProxy): void
  (e: 'test'): void
  (e: 'remove'): void
}>()

const patch = (fields: Partial<EgressProxy>) => {
  if (props.egress) emit('update', { ...props.egress, ...fields })
}
const resultText = (result: EgressTestResult): string => {
  if (!result.ok) return result.error || '失败'
  return [result.ip, result.loc, result.colo, result.ms !== undefined ? `${result.ms}ms` : ''].filter(Boolean).join(' · ')
}
</script>

<template>
  <div class="row">
    <n-space v-if="builtin" :size="6" align="center" :wrap="false">
      <n-tag size="small" :bordered="false" type="info">内置</n-tag>
      <n-text strong>{{ DIRECT_EGRESS_LABEL }}</n-text>
      <n-button size="tiny" :loading="testing" @click="emit('test')">测试</n-button>
    </n-space>
    <n-space v-else-if="egress" :size="6" align="center" :wrap="false">
      <n-input :value="egress.name" size="small" placeholder="名称，如 dmit LA" @update:value="(value: string) => patch({ name: value })" />
      <n-switch :value="egress.enabled" size="small" @update:value="(value: boolean) => patch({ enabled: value })" />
      <n-button size="tiny" :loading="testing" @click="emit('test')">测试</n-button>
      <n-button size="tiny" type="error" secondary @click="emit('remove')">删除</n-button>
    </n-space>
    <n-text v-if="builtin" depth="3" class="hint">本服务器自身的出口 IP</n-text>
    <n-input
      v-else-if="egress"
      :value="egress.url"
      size="small"
      :placeholder="URL_TEXT.proxy"
      @update:value="(value: string) => patch({ url: value })"
    />
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
