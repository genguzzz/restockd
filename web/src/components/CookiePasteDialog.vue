<script setup lang="ts">
// 手动粘贴 Cookie 对话框：支持 header / JSON / cookies.txt 三种格式，可同时提供 UA
import { computed, ref, watch } from 'vue'
import { NAlert, NButton, NForm, NFormItem, NInput, NModal, NSpace, NText } from 'naive-ui'

const props = defineProps<{ show: boolean; title?: string; defaultUserAgent?: string; loading?: boolean }>()
const emit = defineEmits<{
  (e: 'update:show', value: boolean): void
  (e: 'submit', payload: { raw: string; userAgent: string }): void
}>()
const raw = ref('')
const userAgent = ref('')
watch(
  () => props.show,
  (visible) => {
    if (visible) {
      raw.value = ''
      userAgent.value = props.defaultUserAgent ?? ''
    }
  },
)
const format = computed(() => {
  const text = raw.value.trim()
  if (!text) return ''
  if (text.startsWith('[') || text.startsWith('{')) return 'JSON（Cookie-Editor 导出）'
  if (text.includes('\t')) return 'Netscape cookies.txt'
  return '请求头格式 a=1; b=2'
})
const fillBrowserUA = () => {
  userAgent.value = navigator.userAgent
}
const submit = () => emit('submit', { raw: raw.value, userAgent: userAgent.value.trim() })
</script>

<template>
  <n-modal
    :show="show"
    preset="card"
    :title="title ?? '粘贴 Cookie'"
    style="width: min(640px, 94vw)"
    :mask-closable="false"
    @update:show="(value: boolean) => emit('update:show', value)"
  >
    <n-space vertical :size="12">
      <n-alert type="info" :show-icon="false">
        在已登录的浏览器打开站点 → 开发者工具 Network 任一请求复制 <code>Cookie</code> 请求头，或用 Cookie-Editor 导出 JSON。
        <br />含 <code>cf_clearance</code> 时，UA 必须与获取它的浏览器完全一致，且通常绑定出口 IP。
      </n-alert>
      <n-form label-placement="top">
        <n-form-item label="Cookie">
          <n-input v-model:value="raw" type="textarea" :autosize="{ minRows: 5, maxRows: 12 }" placeholder="laravel_session=...; XSRF-TOKEN=...; cf_clearance=..." />
        </n-form-item>
        <n-text v-if="format" depth="3" style="font-size: 12px; margin-top: -12px; display: block">识别为：{{ format }}</n-text>
        <n-form-item label="User-Agent（可选）">
          <n-input v-model:value="userAgent" placeholder="留空则沿用账号当前 UA">
            <template #suffix>
              <n-button text size="tiny" @click="fillBrowserUA">用当前浏览器</n-button>
            </template>
          </n-input>
        </n-form-item>
      </n-form>
    </n-space>
    <template #footer>
      <n-space justify="end">
        <n-button @click="emit('update:show', false)">取消</n-button>
        <n-button type="primary" :disabled="!raw.trim()" :loading="loading" @click="submit">保存</n-button>
      </n-space>
    </template>
  </n-modal>
</template>
