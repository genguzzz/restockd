<script setup lang="ts">
// 设置 · 下单标记有效期（同时是手动锁单的防重复窗口）
import { NAlert, NCard, NForm, NFormItem, NInputNumber } from 'naive-ui'
import type { Settings } from '@/api/types'

const model = defineModel<Settings['orders']>({ required: true })
</script>

<template>
  <n-card title="下单标记" size="small">
    <n-form label-placement="top" size="small">
      <n-alert type="default" :show-icon="false" class="card-note">
        商品上的「已锁单 / 已试下单 / 上次失败」只在有效期内显示；有效期内再次对同一商品真实锁单需要确认。
      </n-alert>
      <n-form-item label="有效期">
        <n-input-number v-model:value="model.mark_hours" :min="1" :max="720" style="width: 100%">
          <template #suffix>小时</template>
        </n-input-number>
      </n-form-item>
    </n-form>
  </n-card>
</template>
