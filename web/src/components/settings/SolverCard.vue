<script setup lang="ts">
// 设置 · 验证码求解（FlareSolverr / 外部服务 / 人工验证超时）
import { NAlert, NCard, NForm, NFormItem, NInput, NInputNumber } from 'naive-ui'
import type { Settings } from '@/api/types'
import SecretInput from '@/components/SecretInput.vue'
import { URL_TEXT } from '@/utils/placeholders'

const model = defineModel<Settings['solver']>({ required: true })
const setTimeoutSec = (value: number | null) => {
  if (value !== null) model.value.manual_timeout_sec = value
}
</script>

<template>
  <n-card title="验证码 / 反爬" size="small">
    <n-form label-placement="top" size="small">
      <n-form-item label="FlareSolverr 地址" feedback="docker run -p 8191:8191 ghcr.io/flaresolverr/flaresolverr">
        <n-input v-model:value="model.flaresolverr_url" :placeholder="URL_TEXT.flaresolverr" />
      </n-form-item>
      <n-form-item label="外部求解服务 URL（可选）">
        <n-input v-model:value="model.external_url" :placeholder="URL_TEXT.externalSolver" />
      </n-form-item>
      <n-form-item label="外部求解服务 Token">
        <SecretInput v-model:value="model.external_token" placeholder="以 Authorization: Bearer 发送" />
      </n-form-item>
      <n-alert type="default" :show-icon="false" class="card-note">
        外部服务约定：<code>POST &lt;URL&gt;</code>，请求
        <code>{"kind","site_key","page_url","action"}</code>，返回
        <code>{"token","cookies","user_agent"}</code>，失败返回 <code>{"error"}</code>。用于 Turnstile / reCAPTCHA / hCaptcha 等 token 类验证。
      </n-alert>
      <n-form-item label="人工验证超时" feedback="自动求解失败后在「人工验证」等待的时长">
        <n-input-number :value="model.manual_timeout_sec" :min="30" :step="30" style="width: 100%" @update:value="setTimeoutSec">
          <template #suffix>秒</template>
        </n-input-number>
      </n-form-item>
    </n-form>
  </n-card>
</template>
