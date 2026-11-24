<script setup lang="ts">
// 计费周期中文名
import { computed } from 'vue'
import { cycleLabel } from '@/utils/format'

const props = defineProps<{ cycle?: string | null }>()
const text = computed(() => cycleLabel(props.cycle))
</script>

<template>
  <span>{{ text }}</span>
</template>
