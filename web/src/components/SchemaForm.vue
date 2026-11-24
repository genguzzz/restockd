<script setup lang="ts">
// 按 Provider 的 config_fields 渲染配置表单，值统一为字符串
import { NFormItem, NInput, NSwitch } from 'naive-ui'
import type { ProviderField } from '@/api/types'

const props = defineProps<{ fields: ProviderField[]; modelValue: Record<string, string> }>()
const emit = defineEmits<{ (e: 'update:modelValue', value: Record<string, string>): void }>()
const update = (key: string, value: string) => emit('update:modelValue', { ...props.modelValue, [key]: value })
const valueOf = (field: ProviderField) => props.modelValue[field.key] ?? field.default ?? ''
</script>

<template>
  <n-form-item
    v-for="field in fields"
    :key="field.key"
    :label="field.label"
    :required="field.required"
    :feedback="field.help"
  >
    <n-switch
      v-if="field.type === 'bool'"
      :value="valueOf(field) === 'true'"
      @update:value="(value: boolean) => update(field.key, value ? 'true' : 'false')"
    />
    <n-input
      v-else
      :value="valueOf(field)"
      :type="field.type === 'password' ? 'password' : 'text'"
      :show-password-on="field.type === 'password' ? 'click' : undefined"
      :placeholder="field.placeholder ?? (field.type === 'list' ? '逗号分隔' : '')"
      @update:value="(value: string) => update(field.key, value)"
    />
  </n-form-item>
</template>
