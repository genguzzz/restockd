<script setup lang="ts">
// 以分钟显示、以秒存储的间隔输入框
import { NInputNumber } from 'naive-ui'

const props = withDefaults(defineProps<{ value: number; min?: number }>(), { min: 1 })
const emit = defineEmits<{ (e: 'update:value', seconds: number): void }>()
// 清空输入时保持原值
const onUpdate = (minutes: number | null) => {
  if (minutes !== null) emit('update:value', minutes * 60)
}
</script>

<template>
  <n-input-number :value="Math.round(props.value / 60)" :min="min" style="width: 100%" @update:value="onUpdate">
    <template #suffix>分钟</template>
  </n-input-number>
</template>
