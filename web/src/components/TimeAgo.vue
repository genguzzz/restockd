<script setup lang="ts">
// 相对时间，每 15 秒刷新；悬停显示绝对时间
import { computed, onBeforeUnmount, ref } from 'vue'
import { formatDateTime, isZeroTime, timeAgo } from '@/utils/format'

const props = defineProps<{ value?: string | null }>()
const now = ref(Date.now())
const timer = setInterval(() => {
  now.value = Date.now()
}, 15000)
onBeforeUnmount(() => clearInterval(timer))
const text = computed(() => (isZeroTime(props.value) ? '-' : timeAgo(props.value, now.value)))
const title = computed(() => (isZeroTime(props.value) ? '' : formatDateTime(props.value)))
</script>

<template>
  <span :title="title" style="white-space: nowrap">{{ text }}</span>
</template>
