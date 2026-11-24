<script setup lang="ts">
// 单个托管账号：状态、登录凭据、余额、Cookie，以及登录 / 校验 / 删除操作
import { computed } from 'vue'
import { NButton, NEllipsis, NSpace, NTag, NText, useMessage, useThemeVars } from 'naive-ui'
import { accountsApi } from '@/api'
import type { Account } from '@/api/types'
import { useAction, useBusy } from '@/composables/useAsync'
import { hasTime } from '@/utils/format'
import { accountStatusMeta, metaOf } from '@/utils/labels'
import ConfirmButton from './ConfirmButton.vue'
import MoneyText from './MoneyText.vue'
import TimeAgo from './TimeAgo.vue'

const COOKIE_PREVIEW_COUNT = 6

// canLogin：站点类型支持账号密码登录（capabilities.login）
const props = defineProps<{ account: Account; canLogin: boolean }>()
const emit = defineEmits<{
  (e: 'changed', account: Account): void
  (e: 'removed', id: string): void
  (e: 'edit'): void
  (e: 'cookies'): void
  (e: 'refresh'): void
}>()

const message = useMessage()
const themeVars = useThemeVars()
const act = useAction()
const { isBusy, track } = useBusy()
const status = computed(() => metaOf(accountStatusMeta, props.account.status))
const suffix = (text?: string) => (text ? `：${text}` : '')

const check = async () => {
  const updated = await track('check', () => act(() => accountsApi.check(props.account.id)))
  if (!updated) return
  emit('changed', updated)
  const label = metaOf(accountStatusMeta, updated.status).label
  if (updated.status === 'active') message.success(`会话${label}${suffix(updated.identity)}`)
  else message.warning(`会话${label}${suffix(updated.last_error)}`)
}

// 用托管的账号密码立即登录（会话失效时引擎也会自动登录）
const login = async () => {
  const updated = await track('login', () => act(() => accountsApi.login(props.account.id)))
  if (updated) {
    emit('changed', updated)
    message.success(`登录成功${suffix(updated.identity)}`)
  } else {
    // 失败时服务端已更新账号状态，刷新一下拿到错误原因
    emit('refresh')
  }
}

const remove = async () => {
  if (await act(() => accountsApi.remove(props.account.id), '已删除')) emit('removed', props.account.id)
}
</script>

<template>
  <div class="account">
    <div class="head">
      <n-text strong class="wrap">{{ account.label || account.id }}</n-text>
      <n-tag :type="status.type" size="small" :bordered="false">{{ status.label }}</n-tag>
      <n-text v-if="account.identity" depth="2" class="wrap">{{ account.identity }}</n-text>
    </div>
    <div class="fields">
      <div v-if="canLogin" class="field">
        <n-text depth="3">登录</n-text>
        <n-space :size="6" align="center">
          <n-text v-if="account.username" class="wrap">{{ account.username }}</n-text>
          <n-tag v-if="account.credentials_rejected" size="tiny" type="error" :bordered="false">密码被拒·已暂停自动登录</n-tag>
          <n-tag v-else-if="account.has_password" size="tiny" type="success" :bordered="false">已托管密码</n-tag>
          <n-text v-else depth="3">未托管密码</n-text>
          <n-text v-if="hasTime(account.login_at)" depth="3">· <TimeAgo :value="account.login_at" /></n-text>
        </n-space>
      </div>
      <div class="field">
        <n-text depth="3">余额</n-text>
        <MoneyText :money="account.balance" />
      </div>
      <div class="field">
        <n-text depth="3">校验</n-text>
        <TimeAgo :value="account.checked_at" />
      </div>
      <div class="field" :title="account.cookie_names.join(', ')">
        <n-text depth="3">Cookie {{ account.cookie_names.length }}</n-text>
        <n-space :size="4">
          <n-tag v-for="name in account.cookie_names.slice(0, COOKIE_PREVIEW_COUNT)" :key="name" size="tiny" :bordered="false">{{ name }}</n-tag>
          <n-text v-if="account.cookie_names.length > COOKIE_PREVIEW_COUNT" depth="3">+{{ account.cookie_names.length - COOKIE_PREVIEW_COUNT }}</n-text>
          <n-text v-if="account.cookie_names.length === 0" depth="3">未设置</n-text>
        </n-space>
      </div>
      <div v-if="account.user_agent" class="field">
        <n-text depth="3">UA</n-text>
        <n-ellipsis class="mono">{{ account.user_agent }}</n-ellipsis>
      </div>
      <div v-if="account.last_error" class="field">
        <n-text depth="3">错误</n-text>
        <n-text type="error"><n-ellipsis :tooltip="{ width: 'trigger' }">{{ account.last_error }}</n-ellipsis></n-text>
      </div>
    </div>
    <n-space :size="8" class="actions">
      <n-button v-if="canLogin && account.has_password" size="small" :loading="isBusy('login')" @click="login">登录</n-button>
      <n-button size="small" @click="emit('cookies')">粘贴 Cookie</n-button>
      <n-button size="small" :loading="isBusy('check')" @click="check">校验</n-button>
      <n-button size="small" @click="emit('edit')">编辑</n-button>
      <ConfirmButton text="删除" type="error" secondary confirm="删除账号及其保存的 Cookie，确定？" :action="remove" />
    </n-space>
  </div>
</template>

<style scoped>
.account {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px;
  border: 1px solid v-bind('themeVars.borderColor');
  border-radius: 6px;
  min-width: 0;
}
.head {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}
.wrap {
  word-break: break-all;
}
.fields {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 13px;
}
.field {
  display: grid;
  grid-template-columns: 72px minmax(0, 1fr);
  gap: 8px;
  align-items: center;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}
.actions {
  margin-top: 4px;
}
</style>
