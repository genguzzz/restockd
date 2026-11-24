<script setup lang="ts">
// 设置 · 无关注站点的降频轮询
import { NAlert, NCard, NForm, NFormItem } from 'naive-ui'
import type { Settings } from '@/api/types'
import MinutesInput from '@/components/MinutesInput.vue'

const model = defineModel<Settings['polling']>({ required: true })
</script>

<template>
  <n-card title="轮询策略" size="small">
    <n-form label-placement="top" size="small">
      <n-alert type="default" :show-icon="false" class="card-note">
        有启用中的关注规则的站点按站点自身间隔轮询；没有关注的站点自动降频，有人打开管理界面时立即刷新一次并提速。
        降频间隔不会低于站点自身设置。
      </n-alert>
      <div class="form-row even">
        <n-form-item label="无人访问时">
          <MinutesInput v-model:value="model.idle_interval_sec" />
        </n-form-item>
        <n-form-item label="有人访问时">
          <MinutesInput v-model:value="model.viewer_interval_sec" />
        </n-form-item>
      </div>
    </n-form>
  </n-card>
</template>
