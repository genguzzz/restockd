<script setup lang="ts">
// 站点页：站点卡片列表 + 新建 / 编辑抽屉；站点列表全局共享并由 SSE 实时更新
import { onMounted, ref } from 'vue'
import { NButton, NCard, NEmpty, NIcon, NSpin } from 'naive-ui'
import { AddOutline, RefreshOutline } from '@vicons/ionicons5'
import { settingsApi } from '@/api'
import type { EgressProxy, Site } from '@/api/types'
import { useAction } from '@/composables/useAsync'
import { useProviders, useSites } from '@/composables/useSites'
import PageHeader from '@/components/PageHeader.vue'
import SiteCard from '@/components/SiteCard.vue'
import SiteFormDrawer from '@/components/SiteFormDrawer.vue'

const act = useAction()
const { sites, loading, reload } = useSites()
const { providers, providerOf } = useProviders()
const proxies = ref<EgressProxy[]>([])

const loadProxies = async () => {
  const settings = await act(() => settingsApi.get())
  if (settings) proxies.value = settings.egress.proxies
}

const refresh = () => {
  void reload()
  void loadProxies()
}

const drawerVisible = ref(false)
const editing = ref<Site | null>(null)
const openDrawer = (site: Site | null) => {
  editing.value = site
  drawerVisible.value = true
}

onMounted(() => {
  void loadProxies()
})
</script>

<template>
  <div class="page">
    <PageHeader title="站点" subtitle="被监控的商家站点及其轮询配置">
      <n-button :loading="loading" @click="refresh">
        <template #icon><n-icon><RefreshOutline /></n-icon></template>
        刷新
      </n-button>
      <n-button type="primary" :disabled="providers.length === 0" @click="openDrawer(null)">
        <template #icon><n-icon><AddOutline /></n-icon></template>
        添加站点
      </n-button>
    </PageHeader>

    <n-spin :show="loading && sites.length === 0">
      <n-card v-if="sites.length === 0" size="small">
        <n-empty description="还没有站点，先添加一个" style="padding: 32px 0" />
      </n-card>
      <div v-else class="grid-cards">
        <SiteCard
          v-for="site in sites"
          :key="site.id"
          :site="site"
          :provider="providerOf(site.kind)"
          :proxies="proxies"
          @edit="openDrawer(site)"
        />
      </div>
    </n-spin>

    <SiteFormDrawer v-model:show="drawerVisible" :site="editing" :proxies="proxies" />
  </div>
</template>
