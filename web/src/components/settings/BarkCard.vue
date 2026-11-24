<script setup lang="ts">
// 设置 · Bark（iOS 推送）
import { NCard, NForm, NFormItem, NInput, NSwitch } from 'naive-ui'
import type { Settings } from '@/api/types'
import SecretInput from '@/components/SecretInput.vue'
import { URL_TEXT } from '@/utils/placeholders'

const model = defineModel<Settings['bark']>({ required: true })
</script>

<template>
  <n-card title="Bark（iOS 推送）" size="small">
    <template #header-extra>
      <n-switch v-model:value="model.enabled" />
    </template>
    <n-form label-placement="top" size="small">
      <n-form-item label="设备地址" :feedback="URL_TEXT.barkHelp">
        <SecretInput v-model:value="model.device_urls" textarea :placeholder="URL_TEXT.barkDevice" />
      </n-form-item>
      <div class="form-row">
        <n-form-item label="分组">
          <n-input v-model:value="model.group" placeholder="restockd" />
        </n-form-item>
        <n-form-item label="铃声">
          <n-input v-model:value="model.sound" placeholder="默认" />
        </n-form-item>
      </div>
      <n-form-item label="图标 URL（可选）" feedback="补货 / 锁单 / 人工验证为「时效性」通知，可突破专注模式">
        <n-input v-model:value="model.icon" :placeholder="URL_TEXT.barkIcon" />
      </n-form-item>
    </n-form>
  </n-card>
</template>
