<script setup lang="ts">
// 页面标题栏：标题、副标题、右侧操作区
import { NText } from 'naive-ui'

defineProps<{ title: string; subtitle?: string }>()
</script>

<template>
  <div class="page-header">
    <div class="titles">
      <h2>{{ title }}</h2>
      <n-text v-if="subtitle" depth="3">{{ subtitle }}</n-text>
    </div>
    <div class="actions"><slot /></div>
  </div>
</template>

<style scoped>
.page-header {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 16px;
}
.titles h2 {
  margin: 0 0 2px;
  font-size: 20px;
  font-weight: 600;
}
.actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
</style>
