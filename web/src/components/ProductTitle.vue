<script setup lang="ts">
// 商品标题：有商品页地址时为外链（新标签打开商家下单页），否则纯文本；点击不冒泡到卡片 / 行的「打开详情」
import { NIcon } from 'naive-ui'
import { OpenOutline } from '@vicons/ionicons5'

defineProps<{ name: string; url?: string }>()
</script>

<template>
  <a v-if="url" :href="url" target="_blank" rel="noopener noreferrer" class="title" title="打开商品页" @click.stop>
    {{ name }}<n-icon class="icon"><OpenOutline /></n-icon>
  </a>
  <span v-else class="title">{{ name }}</span>
</template>

<style scoped>
.title {
  color: inherit;
  text-decoration: none;
  word-break: break-word;
}
a.title:hover {
  color: var(--n-color-target, #18a058);
  text-decoration: underline;
}
.icon {
  margin-left: 4px;
  font-size: 0.85em;
  opacity: 0.55;
  vertical-align: -1px;
}
</style>
