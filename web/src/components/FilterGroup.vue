<script setup lang="ts">
// 多选筛选组：窄屏为一行可横向滑动的芯片，宽屏为竖向勾选列表；一次点击切换，「全部」清空（未选即全部）
import { NCheckbox, NTag, NText } from 'naive-ui'
import { useIsMobile } from '@/composables/useIsMobile'

export interface FilterOption {
  value: string
  label: string
  /** 可选的计数，如商品数 */
  count?: string | number
}

// showAll=false 时不显示「全部」，用作独立开关
const props = withDefaults(defineProps<{ title: string; options: FilterOption[]; value: string[]; showAll?: boolean }>(), { showAll: true })
const emit = defineEmits<{ (e: 'update:value', value: string[]): void }>()
const isMobile = useIsMobile()

const toggle = (value: string) =>
  emit('update:value', props.value.includes(value) ? props.value.filter((item) => item !== value) : [...props.value, value])
const clear = () => emit('update:value', [])
</script>

<template>
  <div v-if="isMobile" class="chips" :aria-label="title">
    <n-tag v-if="showAll" checkable :checked="value.length === 0" @update:checked="clear">全部</n-tag>
    <n-tag v-for="option in options" :key="option.value" checkable :checked="value.includes(option.value)" @update:checked="toggle(option.value)">
      {{ option.label }}<span v-if="option.count !== undefined" class="count">{{ option.count }}</span>
    </n-tag>
  </div>
  <div v-else class="list">
    <n-text depth="3" class="title">{{ title }}</n-text>
    <n-checkbox v-if="showAll" :checked="value.length === 0" @update:checked="clear">全部</n-checkbox>
    <n-checkbox v-for="option in options" :key="option.value" :checked="value.includes(option.value)" @update:checked="toggle(option.value)">
      <span class="item">
        <span class="label">{{ option.label }}</span>
        <n-text v-if="option.count !== undefined" depth="3" class="count">{{ option.count }}</n-text>
      </span>
    </n-checkbox>
  </div>
</template>

<style scoped>
.chips {
  display: flex;
  gap: 6px;
  overflow-x: auto;
  scroll-snap-type: x proximity;
  scrollbar-width: none;
  padding-bottom: 2px;
}
.chips::-webkit-scrollbar {
  display: none;
}
.chips > * {
  flex-shrink: 0;
  scroll-snap-align: start;
}
.count {
  margin-left: 4px;
  opacity: 0.7;
  font-size: 12px;
}
.list {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.title {
  font-size: 12px;
}
.item {
  display: inline-flex;
  gap: 4px;
  word-break: break-word;
}
</style>
