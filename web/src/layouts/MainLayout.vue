<script setup lang="ts">
// 主布局：桌面端可折叠侧栏，移动端抽屉菜单；顶栏含实时连接指示、待处理验证徽标、主题切换、退出
import { computed, h, onMounted, ref, type Component } from 'vue'
import { useRoute, useRouter, RouterLink } from 'vue-router'
import { NBadge, NButton, NDrawer, NDrawerContent, NIcon, NLayout, NLayoutContent, NLayoutHeader, NLayoutSider, NMenu, NTooltip, type MenuOption } from 'naive-ui'
import {
  AlertCircleOutline,
  CartOutline,
  ContrastOutline,
  GridOutline,
  KeyOutline,
  ListOutline,
  LogOutOutline,
  MenuOutline,
  PeopleOutline,
  ReceiptOutline,
  ServerOutline,
  SettingsOutline,
} from '@vicons/ionicons5'
import { authApi } from '@/api'
import { liveConnected, pendingChallengeIds, startLive, stopLive } from '@/composables/useLive'
import { useTheme } from '@/composables/useTheme'
import { useIsMobile } from '@/composables/useIsMobile'

const route = useRoute()
const router = useRouter()
const { mode, cycle } = useTheme()
const collapsed = ref(false)
const drawer = ref(false)
const isMobile = useIsMobile()

const icon = (component: Component) => () => h(NIcon, null, { default: () => h(component) })
const link = (name: string, label: string) => () => h(RouterLink, { to: { name } }, { default: () => label })
const menuOptions = computed<MenuOption[]>(() => [
  { key: 'dashboard', label: link('dashboard', '概览'), icon: icon(GridOutline) },
  { key: 'products', label: link('products', '商品'), icon: icon(CartOutline) },
  { key: 'watches', label: link('watches', '关注与锁单'), icon: icon(ListOutline) },
  { key: 'orders', label: link('orders', '订单'), icon: icon(ReceiptOutline) },
  { key: 'accounts', label: link('accounts', '账号'), icon: icon(PeopleOutline) },
  {
    key: 'challenges',
    label: () =>
      h(RouterLink, { to: { name: 'challenges' } }, {
        default: () => [
          '人工验证',
          pendingChallengeIds.value.length ? h(NBadge, { value: pendingChallengeIds.value.length, type: 'error', style: 'margin-left:8px' }) : null,
        ],
      }),
    icon: icon(KeyOutline),
  },
  { key: 'sites', label: link('sites', '站点'), icon: icon(ServerOutline) },
  { key: 'settings', label: link('settings', '设置'), icon: icon(SettingsOutline) },
])
const activeKey = computed(() => String(route.name ?? 'dashboard'))
const themeText = computed(() => ({ auto: '跟随系统', light: '浅色', dark: '深色' })[mode.value])
const pageTitle = computed(() => (route.meta.title as string | undefined) ?? '')

onMounted(() => {
  startLive()
})

const logout = async () => {
  try {
    await authApi.logout()
  } finally {
    stopLive()
    void router.push({ name: 'login' })
  }
}
</script>

<template>
  <n-layout has-sider position="absolute">
    <n-layout-sider
      v-if="!isMobile"
      bordered
      collapse-mode="width"
      :collapsed-width="64"
      :width="210"
      :collapsed="collapsed"
      show-trigger
      :native-scrollbar="false"
      @collapse="collapsed = true"
      @expand="collapsed = false"
    >
      <div class="brand" :class="{ collapsed }">
        <span class="logo">R</span><span v-if="!collapsed">restockd</span>
      </div>
      <n-menu :value="activeKey" :collapsed="collapsed" :collapsed-width="64" :collapsed-icon-size="20" :options="menuOptions" />
    </n-layout-sider>
    <n-drawer v-model:show="drawer" placement="left" :width="230">
      <n-drawer-content body-content-style="padding: 0">
        <div class="brand"><span class="logo">R</span><span>restockd</span></div>
        <n-menu :value="activeKey" :options="menuOptions" @update:value="drawer = false" />
      </n-drawer-content>
    </n-drawer>
    <n-layout>
      <n-layout-header bordered class="header">
        <div class="left">
          <n-button v-if="isMobile" quaternary circle @click="drawer = true">
            <template #icon><n-icon :component="MenuOutline" /></template>
          </n-button>
          <span class="title">{{ pageTitle }}</span>
        </div>
        <div class="right">
          <n-tooltip>
            <template #trigger>
              <span class="dot" :class="{ on: liveConnected }" />
            </template>
            {{ liveConnected ? '实时连接正常' : '实时连接断开，正在重连' }}
          </n-tooltip>
          <router-link v-if="pendingChallengeIds.length" :to="{ name: 'challenges' }">
            <n-badge :value="pendingChallengeIds.length" type="error">
              <n-button size="small" type="warning" secondary>
                <template #icon><n-icon :component="AlertCircleOutline" /></template>
                待验证
              </n-button>
            </n-badge>
          </router-link>
          <n-tooltip>
            <template #trigger>
              <n-button quaternary circle @click="cycle">
                <template #icon><n-icon :component="ContrastOutline" /></template>
              </n-button>
            </template>
            主题：{{ themeText }}
          </n-tooltip>
          <n-button quaternary circle title="退出" @click="logout">
            <template #icon><n-icon :component="LogOutOutline" /></template>
          </n-button>
        </div>
      </n-layout-header>
      <n-layout-content :native-scrollbar="false" content-style="min-height: calc(100vh - 57px)" class="content">
        <router-view />
      </n-layout-content>
    </n-layout>
  </n-layout>
</template>

<style scoped>
.brand {
  display: flex;
  align-items: center;
  gap: 10px;
  height: 56px;
  padding: 0 20px;
  font-weight: 700;
  font-size: 17px;
  letter-spacing: 0.3px;
}
.brand.collapsed {
  justify-content: center;
  padding: 0;
}
.logo {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border-radius: 7px;
  background: #18a058;
  color: #fff;
  font-size: 15px;
}
.header {
  height: 56px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 16px;
}
.left,
.right {
  display: flex;
  align-items: center;
  gap: 10px;
}
.title {
  font-weight: 600;
}
.dot {
  display: inline-block;
  width: 9px;
  height: 9px;
  border-radius: 50%;
  background: #d03050;
  box-shadow: 0 0 0 3px rgba(208, 48, 80, 0.18);
}
.dot.on {
  background: #18a058;
  box-shadow: 0 0 0 3px rgba(24, 160, 88, 0.18);
}
.content {
  position: absolute;
  top: 57px;
  bottom: 0;
  left: 0;
  right: 0;
}
</style>
