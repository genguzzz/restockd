<script setup lang="ts">
// 商品操作区（卡片底部 / 表格操作列共用）：主按钮「关注 / 已关注」+「⋯」更多（详情下单、商品页、屏蔽）；已屏蔽时只有「恢复」
import { computed } from 'vue'
import { NButton, NDropdown, NIcon, type DropdownOption } from 'naive-ui'
import { EllipsisHorizontal, Star, StarOutline } from '@vicons/ionicons5'
import type { Product } from '@/api/types'

const props = defineProps<{ product: Product }>()
const emit = defineEmits<{
  (e: 'open'): void
  (e: 'watch'): void
  (e: 'hide'): void
  (e: 'restore'): void
}>()

type MoreKey = 'open' | 'page' | 'watch' | 'hide'

const moreOptions = computed<DropdownOption[]>(() => [
  { key: 'open', label: '详情与下单' },
  ...(props.product.url ? [{ key: 'page', label: '打开商品页' }] : []),
  ...(props.product.watched ? [{ key: 'watch', label: '再加一条关注' }] : []),
  { type: 'divider', key: 'd' },
  { key: 'hide', label: '屏蔽此商品' },
])

const onMore = (key: MoreKey) => {
  if (key === 'page' && props.product.url) window.open(props.product.url, '_blank', 'noopener')
  else if (key === 'watch') emit('watch')
  else if (key === 'hide') emit('hide')
  else if (key === 'open') emit('open')
}
</script>

<template>
  <!-- @click.stop：卡片其余区域点击打开详情，操作区不能连带触发 -->
  <div class="actions" @click.stop>
    <n-button v-if="product.hidden" size="small" @click="emit('restore')">恢复</n-button>
    <template v-else>
      <n-button
        size="small"
        :type="product.watched ? 'warning' : 'primary'"
        :secondary="!product.watched"
        :tertiary="product.watched"
        @click="emit('watch')"
      >
        <template #icon>
          <n-icon><Star v-if="product.watched" /><StarOutline v-else /></n-icon>
        </template>
        {{ product.watched ? '已关注' : '关注' }}
      </n-button>
      <n-dropdown trigger="click" placement="bottom-end" :options="moreOptions" @select="onMore">
        <n-button size="small" quaternary circle aria-label="更多操作">
          <template #icon>
            <n-icon><EllipsisHorizontal /></n-icon>
          </template>
        </n-button>
      </n-dropdown>
    </template>
  </div>
</template>

<style scoped>
.actions {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
</style>
