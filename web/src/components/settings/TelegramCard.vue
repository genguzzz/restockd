<script setup lang="ts">
// 设置 · Telegram 通知
import { NCard, NForm, NFormItem, NInput, NSwitch } from 'naive-ui'
import type { Settings } from '@/api/types'
import SecretInput from '@/components/SecretInput.vue'
import { URL_TEXT } from '@/utils/placeholders'

const model = defineModel<Settings['telegram']>({ required: true })
</script>

<template>
  <n-card title="Telegram" size="small">
    <template #header-extra>
      <n-switch v-model:value="model.enabled" />
    </template>
    <n-form label-placement="top" size="small">
      <n-form-item label="Bot Token">
        <SecretInput v-model:value="model.bot_token" placeholder="123456:ABC-DEF..." />
      </n-form-item>
      <n-form-item label="Chat ID" feedback="个人或群组 ID，可通过 @userinfobot 获取">
        <n-input v-model:value="model.chat_id" placeholder="123456789" />
      </n-form-item>
      <n-form-item label="API 地址（可选）" feedback="国内服务器可填反代地址">
        <n-input v-model:value="model.api_base" :placeholder="URL_TEXT.telegramApi" />
      </n-form-item>
    </n-form>
  </n-card>
</template>
