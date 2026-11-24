<script setup lang="ts">
// 设置 · 邮件 SMTP 通知
import { NCard, NForm, NFormItem, NInput, NInputNumber, NSwitch } from 'naive-ui'
import type { Settings } from '@/api/types'
import SecretInput from '@/components/SecretInput.vue'

const model = defineModel<Settings['email']>({ required: true })
const setPort = (value: number | null) => {
  if (value !== null) model.value.port = value
}
</script>

<template>
  <n-card title="邮件 SMTP" size="small">
    <template #header-extra>
      <n-switch v-model:value="model.enabled" />
    </template>
    <n-form label-placement="top" size="small">
      <div class="form-row">
        <n-form-item label="服务器">
          <n-input v-model:value="model.host" placeholder="smtp.gmail.com" />
        </n-form-item>
        <n-form-item label="端口">
          <n-input-number :value="model.port" :min="1" :max="65535" :show-button="false" style="width: 100%" @update:value="setPort" />
        </n-form-item>
      </div>
      <n-form-item label="STARTTLS" feedback="465 端口自动使用 TLS">
        <n-switch v-model:value="model.starttls" />
      </n-form-item>
      <n-form-item label="用户名">
        <n-input v-model:value="model.username" placeholder="me@example.com" />
      </n-form-item>
      <n-form-item label="密码">
        <SecretInput v-model:value="model.password" placeholder="授权码 / 应用专用密码" />
      </n-form-item>
      <n-form-item label="发件人">
        <n-input v-model:value="model.from" placeholder="restockd <me@example.com>" />
      </n-form-item>
      <n-form-item label="收件人" feedback="多个用逗号分隔">
        <n-input v-model:value="model.to" placeholder="a@example.com, b@example.com" />
      </n-form-item>
    </n-form>
  </n-card>
</template>
