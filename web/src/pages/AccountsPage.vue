<script setup lang="ts">
// 账号页：按站点分组展示托管账号，支持账号密码托管（自动登录）、粘贴 Cookie、会话校验、编辑默认下单参数与新建
import { computed, onMounted, ref } from 'vue'
import {
  NAlert,
  NButton,
  NCard,
  NDrawer,
  NDrawerContent,
  NEllipsis,
  NEmpty,
  NForm,
  NFormItem,
  NIcon,
  NInput,
  NSelect,
  NSpace,
  NSpin,
  NSwitch,
  NTag,
  NText,
  useMessage,
  useThemeVars,
  type SelectOption,
} from 'naive-ui'
import { AddOutline, RefreshOutline } from '@vicons/ionicons5'
import { accountsApi, sitesApi } from '@/api'
import type { Account, AccountInput, AccountStatus, OrderDefaults, Site } from '@/api/types'
import { useAction, useAsync } from '@/composables/useAsync'
import PageHeader from '@/components/PageHeader.vue'
import ConfirmButton from '@/components/ConfirmButton.vue'
import CookiePasteDialog from '@/components/CookiePasteDialog.vue'
import MoneyText from '@/components/MoneyText.vue'
import TimeAgo from '@/components/TimeAgo.vue'

type TagType = 'success' | 'error' | 'warning' | 'default'

const COOKIE_PREVIEW_COUNT = 6

const statusMeta: Record<AccountStatus, { type: TagType; label: string }> = {
  active: { type: 'success', label: '有效' },
  expired: { type: 'error', label: '已失效' },
  rate_limited: { type: 'warning', label: '限流' },
  unknown: { type: 'default', label: '未校验' },
}

interface SiteGroup {
  siteId: string
  siteName: string
  accounts: Account[]
}

const message = useMessage()
const themeVars = useThemeVars()
const act = useAction()
const accounts = ref<Account[]>([])
const sites = ref<Site[]>([])
const checking = ref<string | null>(null)
const loggingIn = ref<string | null>(null)

const { loading, run } = useAsync(async () => {
  const [accountList, siteList] = await Promise.all([accountsApi.list(), sitesApi.list()])
  accounts.value = accountList
  sites.value = siteList
  return accountList
})

const siteNames = computed<Record<string, string>>(() => Object.fromEntries(sites.value.map((site) => [site.id, site.name])))
const siteOptions = computed<SelectOption[]>(() => sites.value.map((site) => ({ label: site.name, value: site.id })))

// 按站点分组；站点顺序跟随站点列表，孤立账号排在最后
const groups = computed<SiteGroup[]>(() => {
  const order = sites.value.map((site) => site.id)
  const map = new Map<string, Account[]>()
  for (const account of accounts.value) {
    const list = map.get(account.site_id) ?? []
    list.push(account)
    map.set(account.site_id, list)
  }
  const ids = [...map.keys()].sort((a, b) => {
    const ia = order.indexOf(a)
    const ib = order.indexOf(b)
    return (ia < 0 ? Number.MAX_SAFE_INTEGER : ia) - (ib < 0 ? Number.MAX_SAFE_INTEGER : ib)
  })
  return ids.map((siteId) => ({ siteId, siteName: siteNames.value[siteId] ?? siteId, accounts: map.get(siteId) ?? [] }))
})

const statusOf = (status: AccountStatus) => statusMeta[status] ?? { type: 'default' as TagType, label: status }

const upsertAccount = (account: Account) => {
  const index = accounts.value.findIndex((item) => item.id === account.id)
  if (index >= 0) {
    const next = [...accounts.value]
    next[index] = account
    accounts.value = next
  } else {
    accounts.value = [...accounts.value, account]
  }
}

// ---------- 校验 / 删除 ----------
const checkAccount = async (account: Account) => {
  checking.value = account.id
  const updated = await act(() => accountsApi.check(account.id))
  checking.value = null
  if (!updated) return
  upsertAccount(updated)
  const label = statusOf(updated.status).label
  if (updated.status === 'active') message.success(`会话${label}${updated.identity ? `：${updated.identity}` : ''}`)
  else message.warning(`会话${label}${updated.last_error ? `：${updated.last_error}` : ''}`)
}

// 用托管的账号密码立即登录（会话失效时引擎也会自动登录）
const loginAccount = async (account: Account) => {
  loggingIn.value = account.id
  const updated = await act(() => accountsApi.login(account.id))
  loggingIn.value = null
  if (updated) {
    upsertAccount(updated)
    message.success(`登录成功${updated.identity ? `：${updated.identity}` : ''}`)
    return
  }
  // 失败时服务端已更新账号状态，刷新一下拿到错误原因
  const latest = await accountsApi.list().catch(() => null)
  if (latest) accounts.value = latest
}

const removeAccount = async (account: Account) => {
  const result = await act(() => accountsApi.remove(account.id), '已删除')
  if (result) accounts.value = accounts.value.filter((item) => item.id !== account.id)
}

// ---------- 粘贴 Cookie ----------
const cookieTarget = ref<Account | null>(null)
const cookieVisible = ref(false)
const cookieSaving = ref(false)

const openCookies = (account: Account) => {
  cookieTarget.value = account
  cookieVisible.value = true
}

const submitCookies = async (payload: { raw: string; userAgent: string }) => {
  const target = cookieTarget.value
  if (!target) return
  cookieSaving.value = true
  const result = await act(() => accountsApi.setCookies(target.id, payload.raw, payload.userAgent || undefined))
  cookieSaving.value = false
  if (!result) return
  message.success(`已保存 ${result.cookie_names.length} 个 Cookie：${result.cookie_names.join(', ')}`)
  upsertAccount({ ...target, cookie_names: result.cookie_names, user_agent: payload.userAgent || target.user_agent })
  cookieVisible.value = false
}

// ---------- 新建 / 编辑抽屉 ----------
interface AccountForm {
  site_id: string | null
  label: string
  user_agent: string
  defaults: Required<OrderDefaults>
  cookies_raw: string
  username: string
  password: string
  hasPassword: boolean
  clearPassword: boolean
}

const emptyForm = (): AccountForm => ({
  site_id: null,
  label: '',
  user_agent: '',
  defaults: { gateway: '', os: '', hostname: '', ssh_key: '' },
  cookies_raw: '',
  username: '',
  password: '',
  hasPassword: false,
  clearPassword: false,
})

const drawerVisible = ref(false)
const editingId = ref<string | null>(null)
const saving = ref(false)
const form = ref<AccountForm>(emptyForm())

const openCreate = () => {
  editingId.value = null
  form.value = { ...emptyForm(), site_id: sites.value[0]?.id ?? null }
  drawerVisible.value = true
}

const openEdit = (account: Account) => {
  editingId.value = account.id
  form.value = {
    site_id: account.site_id,
    label: account.label,
    user_agent: account.user_agent ?? '',
    defaults: {
      gateway: account.defaults?.gateway ?? '',
      os: account.defaults?.os ?? '',
      hostname: account.defaults?.hostname ?? '',
      ssh_key: account.defaults?.ssh_key ?? '',
    },
    cookies_raw: '',
    username: account.username ?? '',
    password: '',
    hasPassword: account.has_password,
    clearPassword: false,
  }
  drawerVisible.value = true
}

const fillBrowserUA = () => {
  form.value.user_agent = navigator.userAgent
}

const submit = async () => {
  const current = form.value
  if (!current.site_id) {
    message.warning('请选择站点')
    return
  }
  if (!current.label.trim()) {
    message.warning('请填写备注名')
    return
  }
  const input: AccountInput = {
    site_id: current.site_id,
    label: current.label.trim(),
    user_agent: current.user_agent.trim(),
    defaults: { ...current.defaults },
    username: current.username.trim(),
  }
  // 密码：输入了就更新；勾选清除则清空；否则不传（保留已保存的密码）
  if (current.password) input.password = current.password
  else if (current.clearPassword) input.password = ''
  const id = editingId.value
  if (!id && current.cookies_raw.trim()) input.cookies_raw = current.cookies_raw.trim()
  saving.value = true
  const saved = await act(() => (id ? accountsApi.update(id, { ...input, id }) : accountsApi.create(input)), id ? '已保存' : '已添加')
  saving.value = false
  if (!saved) return
  upsertAccount(saved)
  drawerVisible.value = false
}

onMounted(() => {
  void run()
})
</script>

<template>
  <div class="page">
    <PageHeader title="账号" subtitle="用于登录态监控与自动锁单的站点账号">
      <n-button :loading="loading" @click="run()">
        <template #icon><n-icon><RefreshOutline /></n-icon></template>
        刷新
      </n-button>
      <n-button type="primary" :disabled="sites.length === 0" @click="openCreate">
        <template #icon><n-icon><AddOutline /></n-icon></template>
        添加账号
      </n-button>
    </PageHeader>

    <n-alert type="info" :show-icon="false" class="notice">
      推荐托管账号密码：会话失效时自动登录（验证码走「设置 → 验证码」里的求解服务），无需手动续 Cookie。
      密码与 Cookie 只保存在本服务器，不会回传到浏览器。<code>cf_clearance</code> 与 UA 和出口 IP 绑定：Cloudflare 站点优先使用 FlareSolverr。
    </n-alert>

    <n-spin :show="loading && accounts.length === 0">
      <n-card v-if="groups.length === 0" size="small">
        <n-empty :description="sites.length === 0 ? '请先添加站点' : '还没有账号'" style="padding: 32px 0" />
      </n-card>
      <div v-else class="groups">
        <n-card v-for="group in groups" :key="group.siteId" :title="group.siteName" size="small">
          <div class="accounts">
            <div v-for="account in group.accounts" :key="account.id" class="account">
              <div class="head">
                <n-text strong class="label">{{ account.label || account.id }}</n-text>
                <n-tag :type="statusOf(account.status).type" size="small" :bordered="false">{{ statusOf(account.status).label }}</n-tag>
                <n-text v-if="account.identity" depth="2" class="identity">{{ account.identity }}</n-text>
              </div>
              <div class="fields">
                <div class="field">
                  <n-text depth="3">登录</n-text>
                  <n-space :size="6" align="center">
                    <n-text v-if="account.username" class="identity">{{ account.username }}</n-text>
                    <n-tag v-if="account.credentials_rejected" size="tiny" type="error" :bordered="false">密码被拒·已暂停自动登录</n-tag>
                    <n-tag v-else-if="account.has_password" size="tiny" type="success" :bordered="false">已托管密码</n-tag>
                    <n-text v-else depth="3">未托管密码</n-text>
                    <n-text v-if="account.login_at" depth="3">· <TimeAgo :value="account.login_at" /></n-text>
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
                <div class="field cookies" :title="account.cookie_names.join(', ')">
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
                <n-button v-if="account.has_password" size="small" :loading="loggingIn === account.id" @click="loginAccount(account)">登录</n-button>
                <n-button size="small" @click="openCookies(account)">粘贴 Cookie</n-button>
                <n-button size="small" :loading="checking === account.id" @click="checkAccount(account)">校验</n-button>
                <n-button size="small" @click="openEdit(account)">编辑</n-button>
                <ConfirmButton text="删除" type="error" secondary confirm="删除账号及其保存的 Cookie，确定？" :action="() => removeAccount(account)" />
              </n-space>
            </div>
          </div>
        </n-card>
      </div>
    </n-spin>

    <CookiePasteDialog
      v-model:show="cookieVisible"
      :title="cookieTarget ? `粘贴 Cookie · ${cookieTarget.label}` : '粘贴 Cookie'"
      :default-user-agent="cookieTarget?.user_agent ?? ''"
      :loading="cookieSaving"
      @submit="submitCookies"
    />

    <n-drawer v-model:show="drawerVisible" width="min(560px, 100vw)">
      <n-drawer-content :title="editingId ? '编辑账号' : '添加账号'" closable>
        <n-form label-placement="top">
          <n-form-item label="站点" required>
            <n-select v-model:value="form.site_id" :options="siteOptions" :disabled="Boolean(editingId)" placeholder="选择站点" />
          </n-form-item>
          <n-form-item label="备注名" required>
            <n-input v-model:value="form.label" placeholder="例如 主号" />
          </n-form-item>
          <n-text depth="3" class="section">登录凭据（托管后会话失效自动登录）</n-text>
          <div class="row-2">
            <n-form-item label="用户名 / 邮箱">
              <n-input v-model:value="form.username" placeholder="me@example.com" :input-props="{ autocomplete: 'off' }" />
            </n-form-item>
            <n-form-item label="密码">
              <n-input
                v-model:value="form.password"
                type="password"
                show-password-on="click"
                :disabled="form.clearPassword"
                :placeholder="form.hasPassword ? '已保存（留空不修改）' : '可选'"
                :input-props="{ autocomplete: 'new-password' }"
              />
            </n-form-item>
          </div>
          <n-form-item v-if="form.hasPassword" label="清除已保存的密码" label-placement="left">
            <n-switch v-model:value="form.clearPassword" />
          </n-form-item>
          <n-form-item label="User-Agent" feedback="使用 cf_clearance 时必须与获取 Cookie 的浏览器一致">
            <n-input v-model:value="form.user_agent" placeholder="留空使用默认 UA">
              <template #suffix>
                <n-button text size="tiny" @click="fillBrowserUA">用当前浏览器</n-button>
              </template>
            </n-input>
          </n-form-item>
          <n-text depth="3" class="section">默认下单参数（关注规则未指定时使用）</n-text>
          <div class="row-2">
            <n-form-item label="支付方式">
              <n-input v-model:value="form.defaults.gateway" placeholder="gateway 值" />
            </n-form-item>
            <n-form-item label="操作系统">
              <n-input v-model:value="form.defaults.os" placeholder="例如 debian-12" />
            </n-form-item>
          </div>
          <n-form-item label="主机名">
            <n-input v-model:value="form.defaults.hostname" placeholder="可选" />
          </n-form-item>
          <n-form-item label="SSH 公钥">
            <n-input v-model:value="form.defaults.ssh_key" type="textarea" :autosize="{ minRows: 2, maxRows: 6 }" placeholder="ssh-ed25519 AAAA..." />
          </n-form-item>
          <n-form-item v-if="!editingId" label="Cookie（可选）" feedback="支持 a=1; b=2、Cookie-Editor JSON、cookies.txt；也可稍后用「粘贴 Cookie」设置">
            <n-input v-model:value="form.cookies_raw" type="textarea" :autosize="{ minRows: 3, maxRows: 8 }" placeholder="laravel_session=...; XSRF-TOKEN=..." />
          </n-form-item>
        </n-form>
        <template #footer>
          <n-space justify="end">
            <n-button @click="drawerVisible = false">取消</n-button>
            <n-button type="primary" :loading="saving" @click="submit">保存</n-button>
          </n-space>
        </template>
      </n-drawer-content>
    </n-drawer>
  </div>
</template>

<style scoped>
.notice {
  margin-bottom: 14px;
}
.groups {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.accounts {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(min(360px, 100%), 1fr));
  gap: 12px;
}
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
.label,
.identity {
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
.section {
  display: block;
  margin: 4px 0 10px;
  font-size: 13px;
}
.row-2 {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
  gap: 0 12px;
}
</style>
