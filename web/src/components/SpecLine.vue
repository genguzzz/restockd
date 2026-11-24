<script setup lang="ts">
// 规格一行展示：CPU / 内存 / 硬盘 / 带宽 / 流量 + 位置、IP 类型
import { computed } from 'vue'
import { NSpace, NText } from 'naive-ui'
import type { Specs } from '@/api/types'
import { specsSummary } from '@/utils/format'

const props = defineProps<{ specs: Specs; compact?: boolean }>()
const summary = computed(() => specsSummary(props.specs))
</script>

<template>
  <n-space vertical :size="2">
    <n-text>{{ summary || '规格未知' }}</n-text>
    <n-text v-if="!compact && (specs.location || specs.ip_type)" depth="3" style="font-size: 12px">
      <span v-if="specs.location">{{ specs.location }}</span>
      <span v-if="specs.location && specs.ip_type"> · </span>
      <span v-if="specs.ip_type">{{ specs.ip_type }}</span>
    </n-text>
  </n-space>
</template>
