<script setup lang="ts">
// 轮询间隔快捷按钮（1 / 3 分钟），低于 Provider 最小间隔的不可选
import { NButton, NSpace } from 'naive-ui'
import { formatInterval } from '@/utils/format'

const QUICK_INTERVALS_SEC = [60, 180]

withDefaults(defineProps<{ value: number; minSec?: number; size?: 'tiny' | 'small'; disabled?: boolean }>(), {
  minSec: 0,
  size: 'tiny',
})
const emit = defineEmits<{ (e: 'select', seconds: number): void }>()
</script>

<template>
  <n-space :size="6" :wrap="false">
    <n-button
      v-for="sec in QUICK_INTERVALS_SEC"
      :key="sec"
      :size="size"
      :type="value === sec ? 'primary' : 'default'"
      :secondary="value !== sec"
      :disabled="disabled || minSec > sec"
      @click="value !== sec && emit('select', sec)"
    >
      {{ formatInterval(sec) }}
    </n-button>
  </n-space>
</template>
