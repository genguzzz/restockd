<script setup lang="ts">
// 密钥输入框：服务端已设置时回传 "******"，此时显示为空 + 占位提示；
// 不输入则原样回传 "******"（不修改），「清除」回传空串，输入新值则覆盖
import { computed, ref, watch } from 'vue'
import { NButton, NInput } from 'naive-ui'
import { SECRET_MASK } from '@/api'

const props = defineProps<{ value: string; placeholder?: string; textarea?: boolean }>()
const emit = defineEmits<{ (e: 'update:value', value: string): void }>()

// 服务端是否保存着值：外部传入 "******" 为是，传入空串（清除 / 换了表单）为否，输入中的新值不改变它
const stored = ref(props.value === SECRET_MASK)
watch(
  () => props.value,
  (value) => {
    if (value === SECRET_MASK) stored.value = true
    else if (value === '') stored.value = false
  },
)

const masked = computed(() => props.value === SECRET_MASK)
const shown = computed(() => (masked.value ? '' : props.value))
const hint = computed(() => (masked.value ? '已设置（留空不修改）' : props.placeholder))

// 删光输入时回到「不修改」
const onInput = (value: string) => emit('update:value', value === '' && stored.value ? SECRET_MASK : value)
const clear = () => emit('update:value', '')
</script>

<template>
  <div class="secret">
    <n-input
      v-if="textarea"
      :value="shown"
      type="textarea"
      :autosize="{ minRows: 1, maxRows: 4 }"
      :placeholder="hint"
      @update:value="onInput"
    />
    <n-input
      v-else
      :value="shown"
      type="password"
      show-password-on="click"
      :placeholder="hint"
      :input-props="{ autocomplete: 'new-password' }"
      @update:value="onInput"
    />
    <n-button v-if="masked" size="small" quaternary @click="clear">清除</n-button>
  </div>
</template>

<style scoped>
.secret {
  display: flex;
  gap: 6px;
  align-items: flex-start;
  width: 100%;
}
</style>
