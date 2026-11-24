<script setup lang="ts">
// User-Agent 输入框，带「用当前浏览器」快捷填充
import { NButton, NInput } from 'naive-ui'

defineProps<{ value: string; placeholder?: string }>()
const emit = defineEmits<{ (e: 'update:value', value: string): void }>()
const fillBrowser = () => emit('update:value', navigator.userAgent)
</script>

<template>
  <n-input :value="value" :placeholder="placeholder" @update:value="(text: string) => emit('update:value', text)">
    <template #suffix>
      <n-button text size="tiny" @click="fillBrowser">用当前浏览器</n-button>
    </template>
  </n-input>
</template>
