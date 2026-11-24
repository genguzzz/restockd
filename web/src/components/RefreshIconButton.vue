<script setup lang="ts">
// 圆形刷新图标按钮（立即轮询）；点击不冒泡，避免触发卡片 / 行的「打开详情」
import { NButton, NIcon } from 'naive-ui'
import { RefreshOutline } from '@vicons/ionicons5'

defineProps<{ loading?: boolean; title?: string }>()
const emit = defineEmits<{ (e: 'click'): void }>()
</script>

<template>
  <n-button
    size="tiny"
    quaternary
    circle
    :loading="loading"
    :title="title ?? '立即刷新'"
    :aria-label="title ?? '立即刷新'"
    @click.stop="emit('click')"
  >
    <template #icon>
      <n-icon><RefreshOutline /></n-icon>
    </template>
  </n-button>
</template>
