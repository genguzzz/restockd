<script setup lang="ts">
// 事件流列表：类型标签 + 标题 + 详情 + 时间
import { NEmpty, NTag, NText } from 'naive-ui'
import type { AppEvent } from '@/api/types'
import { eventMeta, metaOf } from '@/utils/labels'
import TimeAgo from './TimeAgo.vue'

defineProps<{ events: AppEvent[]; siteNames?: Record<string, string> }>()
</script>

<template>
  <n-empty v-if="events.length === 0" description="暂无事件" style="padding: 24px 0" />
  <div v-else class="feed">
    <div v-for="event in events" :key="event.id" class="row">
      <n-tag :type="metaOf(eventMeta, event.type).type" size="small" :bordered="false">{{ metaOf(eventMeta, event.type).label }}</n-tag>
      <div class="body">
        <div class="title">
          <n-text v-if="siteNames?.[event.site_id]" depth="3">[{{ siteNames[event.site_id] }}] </n-text>{{ event.title }}
        </div>
        <n-text v-if="event.detail" depth="3" class="detail">{{ event.detail }}</n-text>
      </div>
      <n-text depth="3" class="time"><TimeAgo :value="event.created_at" /></n-text>
    </div>
  </div>
</template>

<style scoped>
.feed {
  display: flex;
  flex-direction: column;
}
.row {
  display: grid;
  grid-template-columns: auto 1fr auto;
  gap: 10px;
  align-items: start;
  padding: 8px 0;
  border-bottom: 1px solid var(--n-border-color, rgba(128, 128, 128, 0.15));
}
.row:last-child {
  border-bottom: none;
}
.title {
  word-break: break-word;
}
.detail {
  display: block;
  font-size: 12px;
  word-break: break-all;
}
.time {
  font-size: 12px;
}
</style>
