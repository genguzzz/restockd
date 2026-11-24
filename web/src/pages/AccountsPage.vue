<script setup lang="ts">
// 账号页：按站点分组展示托管账号，实时同步状态；登录 / 校验 / 编辑等操作在 AccountCard 与 AccountFormDrawer 里
import { computed, onMounted, ref } from 'vue'
import { NAlert, NButton, NCard, NEmpty, NIcon, NSpin, useMessage } from 'naive-ui'
import { AddOutline, RefreshOutline } from '@vicons/ionicons5'
import { accountsApi } from '@/api'
import type { Account } from '@/api/types'
import { useAction, useAsync, useBusy } from '@/composables/useAsync'
import { onLive } from '@/composables/useLive'
import { useProviders, useSites } from '@/composables/useSites'
import { removeById, upsertById } from '@/utils/list'
import PageHeader from '@/components/PageHeader.vue'
import AccountCard from '@/components/AccountCard.vue'
import AccountFormDrawer from '@/components/AccountFormDrawer.vue'
import CookiePasteDialog from '@/components/CookiePasteDialog.vue'

interface SiteGroup {
  siteId: string
  siteName: string
  canLogin: boolean
  accounts: Account[]
}

const message = useMessage()
const act = useAction()
const { isBusy, track } = useBusy()
const { sites, siteNames } = useSites()
const { providerOf } = useProviders()
const accounts = ref<Account[]>([])

const { loading, run } = useAsync(async () => {
  accounts.value = await accountsApi.list()
  return accounts.value
})

const upsertAccount = (account: Account) => {
  accounts.value = upsertById(accounts.value, account)
}
onLive('account', upsertAccount)

const canLoginSite = (siteId: string): boolean => {
  const site = sites.value.find((item) => item.id === siteId)
  return Boolean(site && providerOf(site.kind)?.capabilities.login)
}

// 按站点分组；站点顺序跟随站点列表，孤立账号排在最后
const groups = computed<SiteGroup[]>(() => {
  const order = sites.value.map((site) => site.id)
  const map = new Map<string, Account[]>()
  for (const account of accounts.value) map.set(account.site_id, [...(map.get(account.site_id) ?? []), account])
  const rank = (id: string) => (order.includes(id) ? order.indexOf(id) : Number.MAX_SAFE_INTEGER)
  return [...map.keys()]
    .sort((a, b) => rank(a) - rank(b))
    .map((siteId) => ({ siteId, siteName: siteNames.value[siteId] ?? siteId, canLogin: canLoginSite(siteId), accounts: map.get(siteId) ?? [] }))
})

// ---------- 粘贴 Cookie ----------
const cookieTarget = ref<Account | null>(null)
const cookieVisible = ref(false)

const openCookies = (account: Account) => {
  cookieTarget.value = account
  cookieVisible.value = true
}

const submitCookies = async (payload: { raw: string; userAgent: string }) => {
  const target = cookieTarget.value
  if (!target) return
  const result = await track('cookies', () => act(() => accountsApi.setCookies(target.id, payload.raw, payload.userAgent || undefined)))
  if (!result) return
  message.success(`已保存 ${result.cookie_names.length} 个 Cookie：${result.cookie_names.join(', ')}`)
  upsertAccount({ ...target, cookie_names: result.cookie_names, user_agent: payload.userAgent || target.user_agent })
  cookieVisible.value = false
}

// ---------- 新建 / 编辑抽屉 ----------
const drawerVisible = ref(false)
const editing = ref<Account | null>(null)
const openDrawer = (account: Account | null) => {
  editing.value = account
  drawerVisible.value = true
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
      <n-button type="primary" :disabled="sites.length === 0" @click="openDrawer(null)">
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
            <AccountCard
              v-for="account in group.accounts"
              :key="account.id"
              :account="account"
              :can-login="group.canLogin"
              @changed="upsertAccount"
              @removed="(id: string) => (accounts = removeById(accounts, id))"
              @refresh="run()"
              @edit="openDrawer(account)"
              @cookies="openCookies(account)"
            />
          </div>
        </n-card>
      </div>
    </n-spin>

    <CookiePasteDialog
      v-model:show="cookieVisible"
      :title="cookieTarget ? `粘贴 Cookie · ${cookieTarget.label}` : '粘贴 Cookie'"
      :default-user-agent="cookieTarget?.user_agent ?? ''"
      :loading="isBusy('cookies')"
      @submit="submitCookies"
    />

    <AccountFormDrawer v-model:show="drawerVisible" :account="editing" @saved="upsertAccount" />
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
</style>
