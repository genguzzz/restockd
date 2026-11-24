<script setup lang="ts">
// 根组件：主题与全局 Provider（消息、对话框、通知）
import { NConfigProvider, NDialogProvider, NGlobalStyle, NLoadingBarProvider, NMessageProvider, NNotificationProvider, dateZhCN, zhCN, type GlobalThemeOverrides } from 'naive-ui'
import { useTheme } from '@/composables/useTheme'

const { theme } = useTheme()
const overrides: GlobalThemeOverrides = {
  common: { primaryColor: '#18a058', primaryColorHover: '#36ad6a', primaryColorPressed: '#0c7a43', borderRadius: '6px' },
}
</script>

<template>
  <n-config-provider :theme="theme" :theme-overrides="overrides" :locale="zhCN" :date-locale="dateZhCN">
    <n-global-style />
    <n-loading-bar-provider>
      <n-message-provider>
        <n-notification-provider>
          <n-dialog-provider>
            <router-view />
          </n-dialog-provider>
        </n-notification-provider>
      </n-message-provider>
    </n-loading-bar-provider>
  </n-config-provider>
</template>
