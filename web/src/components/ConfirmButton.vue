<script setup lang="ts">
// 带二次确认的按钮：确认后执行 action，执行期间显示加载态
import { ref } from 'vue'
import { NButton, NPopconfirm } from 'naive-ui'

const props = defineProps<{
  text: string
  confirm: string
  action: () => Promise<unknown> | unknown
  type?: 'default' | 'primary' | 'error' | 'warning' | 'success' | 'info'
  size?: 'tiny' | 'small' | 'medium'
  secondary?: boolean
  disabled?: boolean
}>()
const loading = ref(false)
const run = async () => {
  loading.value = true
  try {
    await props.action()
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <n-popconfirm @positive-click="run">
    <template #trigger>
      <n-button :type="type ?? 'default'" :size="size ?? 'small'" :secondary="secondary" :disabled="disabled" :loading="loading">
        {{ text }}
      </n-button>
    </template>
    {{ confirm }}
  </n-popconfirm>
</template>
