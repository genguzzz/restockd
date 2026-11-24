<script setup lang="ts">
// 单个站点卡片：启停、轮询间隔快捷设置、出口、轮询状态、立即轮询 / 编辑 / 删除
import { computed } from 'vue'
import { NButton, NCard, NEllipsis, NSpace, NSwitch, NTag, NText, useMessage } from 'naive-ui'
import { sitesApi } from '@/api'
import type { EgressProxy, ProviderMeta, Site, SiteInput } from '@/api/types'
import { useAction, useBusy } from '@/composables/useAsync'
import { removeSite, toSiteInput, upsertSite } from '@/composables/useSites'
import { egressLabel } from '@/utils/egress'
import { formatInterval } from '@/utils/format'
import { fetchModeMeta, metaOf } from '@/utils/labels'
import ConfirmButton from './ConfirmButton.vue'
import QuickIntervalButtons from './QuickIntervalButtons.vue'
import TimeAgo from './TimeAgo.vue'

const props = defineProps<{ site: Site; provider?: ProviderMeta; proxies: EgressProxy[] }>()
const emit = defineEmits<{ (e: 'edit'): void }>()

const message = useMessage()
const act = useAction()
const { isBusy, track } = useBusy()
const fetchMode = computed(() => metaOf(fetchModeMeta, props.site.fetch_mode))
const egressText = computed(() => (props.site.egress ?? []).map((id) => egressLabel(id, props.proxies)).join(' → '))

const save = async (key: string, patch: Partial<SiteInput>, success: string) => {
  const saved = await track(key, () => act(() => sitesApi.update(props.site.id, { ...toSiteInput(props.site), ...patch }), success))
  if (saved) upsertSite(saved)
}
const toggleEnabled = (enabled: boolean) => save('enabled', { enabled }, enabled ? '已启用' : '已停用')
const setIntervalSec = (sec: number) => save('interval', { interval_sec: sec }, `已改为每 ${formatInterval(sec)}`)

const pollNow = async () => {
  const result = await track('poll', () => act(() => sitesApi.poll(props.site.id)))
  if (result) message.success(`轮询完成，共 ${result.products} 个商品`)
}

const remove = async () => {
  if (await act(() => sitesApi.remove(props.site.id), '已删除')) removeSite(props.site.id)
}
</script>

<template>
  <n-card size="small">
    <template #header>
      <div class="title">
        <span>{{ site.name }}</span>
        <n-tag size="small" :bordered="false">{{ provider?.name ?? site.kind }}</n-tag>
      </div>
    </template>
    <template #header-extra>
      <n-switch :value="site.enabled" :loading="isBusy('enabled')" size="small" @update:value="toggleEnabled" />
    </template>
    <div class="info">
      <a :href="site.base_url" target="_blank" rel="noopener noreferrer" class="link">{{ site.base_url }}</a>
      <n-space :size="6" align="center">
        <n-tag size="small" :type="fetchMode.type" :bordered="false">{{ fetchMode.label }}</n-tag>
        <n-text depth="3">每 {{ formatInterval(site.interval_sec) }}</n-text>
        <QuickIntervalButtons
          :value="site.interval_sec"
          :min-sec="provider?.min_interval_sec"
          :disabled="isBusy('interval')"
          @select="setIntervalSec"
        />
      </n-space>
      <n-text v-if="egressText" depth="3" class="small">出口：{{ egressText }}</n-text>
      <div class="stats">
        <span><n-text depth="3">商品 </n-text>{{ site.product_count }}</span>
        <span><n-text depth="3">有货 </n-text><n-text :type="site.in_stock_count > 0 ? 'success' : 'default'" strong>{{ site.in_stock_count }}</n-text></span>
        <span><n-text depth="3">上次轮询 </n-text><TimeAgo :value="site.last_poll_at" /></span>
      </div>
      <n-text v-if="site.last_error" type="error" class="small">
        <n-ellipsis :line-clamp="2" :tooltip="{ width: 'trigger' }">
          {{ site.consecutive_fail > 1 ? `连续失败 ${site.consecutive_fail} 次：` : '' }}{{ site.last_error }}
        </n-ellipsis>
      </n-text>
    </div>
    <template #action>
      <n-space justify="end" :size="8">
        <n-button size="small" :loading="isBusy('poll')" @click="pollNow">立即轮询</n-button>
        <n-button size="small" @click="emit('edit')">编辑</n-button>
        <ConfirmButton text="删除" type="error" secondary confirm="删除站点会同时删除其账号、关注规则和商品快照，确定？" :action="remove" />
      </n-space>
    </template>
  </n-card>
</template>

<style scoped>
.title {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}
.info {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.link {
  word-break: break-all;
}
.stats {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 16px;
}
.small {
  font-size: 12px;
}
</style>
