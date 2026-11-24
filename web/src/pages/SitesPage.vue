<script setup lang="ts">
// 站点页：站点卡片（启停、轮询状态、立即轮询）与按 Provider 配置渲染的新建 / 编辑抽屉
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
  NInputNumber,
  NRadio,
  NRadioGroup,
  NSelect,
  NSpace,
  NSpin,
  NSwitch,
  NTag,
  NText,
  useMessage,
  type SelectOption,
} from 'naive-ui'
import { AddOutline, RefreshOutline } from '@vicons/ionicons5'
import { accountsApi, providersApi, sitesApi } from '@/api'
import type { Account, FetchMode, ProviderMeta, Site, SiteInput } from '@/api/types'
import { useAction, useAsync } from '@/composables/useAsync'
import { onLive } from '@/composables/useLive'
import PageHeader from '@/components/PageHeader.vue'
import ConfirmButton from '@/components/ConfirmButton.vue'
import SchemaForm from '@/components/SchemaForm.vue'
import TimeAgo from '@/components/TimeAgo.vue'

const DEFAULT_INTERVAL_SEC = 60

const fetchModeLabels: Record<FetchMode, string> = { direct: '直连', flaresolverr: 'FlareSolverr' }
const capabilityLabels: Record<keyof ProviderMeta['capabilities'], string> = {
  catalog: '商品目录',
  coupon: '优惠码',
  order: '自动下单',
  session_check: '会话校验',
  stock_needs_login: '库存需登录',
}

const message = useMessage()
const act = useAction()
const sites = ref<Site[]>([])
const providers = ref<ProviderMeta[]>([])
const polling = ref<string | null>(null)
const toggling = ref<string | null>(null)
const providerOf = (kind: string): ProviderMeta | undefined => providers.value.find((item) => item.kind === kind)

const { loading, run } = useAsync(async () => {
  const [siteList, providerList] = await Promise.all([sitesApi.list(), providersApi.list()])
  sites.value = siteList
  providers.value = providerList
  return siteList
})

const upsertSite = (site: Site) => {
  const index = sites.value.findIndex((item) => item.id === site.id)
  if (index >= 0) {
    const next = [...sites.value]
    next[index] = site
    sites.value = next
  } else {
    sites.value = [...sites.value, site]
  }
}
onLive('site', upsertSite)

// 只取可写字段，避免把只读状态回传
const toInput = (site: Site): SiteInput => ({
  id: site.id,
  kind: site.kind,
  name: site.name,
  base_url: site.base_url,
  config: { ...site.config },
  interval_sec: site.interval_sec,
  fetch_mode: site.fetch_mode,
  monitor_account_id: site.monitor_account_id ?? '',
  enabled: site.enabled,
})

const toggleEnabled = async (site: Site, enabled: boolean) => {
  toggling.value = site.id
  const saved = await act(() => sitesApi.update(site.id, { ...toInput(site), enabled }), enabled ? '已启用' : '已停用')
  toggling.value = null
  if (saved) upsertSite(saved)
}

const pollNow = async (site: Site) => {
  polling.value = site.id
  const result = await act(() => sitesApi.poll(site.id))
  polling.value = null
  if (result) message.success(`轮询完成，共 ${result.products} 个商品`)
}

const removeSite = async (site: Site) => {
  const result = await act(() => sitesApi.remove(site.id), '已删除')
  if (result) sites.value = sites.value.filter((item) => item.id !== site.id)
}

// ---------- 抽屉表单 ----------
const drawerVisible = ref(false)
const editingId = ref<string | null>(null)
const saving = ref(false)
const form = ref<SiteInput>(emptyInput())
const siteAccounts = ref<Account[]>([])

function emptyInput(): SiteInput {
  return { kind: '', name: '', base_url: '', config: {}, interval_sec: DEFAULT_INTERVAL_SEC, fetch_mode: 'direct', monitor_account_id: '', enabled: true }
}

const selectedProvider = computed<ProviderMeta | undefined>(() => providerOf(form.value.kind))
const providerOptions = computed<SelectOption[]>(() => providers.value.map((item) => ({ label: `${item.name}（${item.kind}）`, value: item.kind })))
const accountOptions = computed<SelectOption[]>(() =>
  siteAccounts.value.map((item) => ({ label: item.identity ? `${item.label}（${item.identity}）` : item.label, value: item.id })),
)
const enabledCapabilities = computed<string[]>(() => {
  const provider = selectedProvider.value
  if (!provider) return []
  return (Object.keys(capabilityLabels) as (keyof ProviderMeta['capabilities'])[])
    .filter((key) => provider.capabilities[key])
    .map((key) => capabilityLabels[key])
})

// 切换类型时按 Provider 默认值初始化配置、间隔与抓取方式
const onKindChange = (kind: string) => {
  const provider = providerOf(kind)
  form.value.kind = kind
  if (!provider) return
  form.value.config = Object.fromEntries(provider.config_fields.map((field) => [field.key, field.default ?? '']))
  form.value.interval_sec = provider.default_interval_sec || DEFAULT_INTERVAL_SEC
  form.value.fetch_mode = provider.default_fetch_mode
}

const openCreate = () => {
  editingId.value = null
  form.value = emptyInput()
  siteAccounts.value = []
  const first = providers.value[0]
  if (first) onKindChange(first.kind)
  drawerVisible.value = true
}

const openEdit = async (site: Site) => {
  editingId.value = site.id
  form.value = toInput(site)
  siteAccounts.value = []
  drawerVisible.value = true
  const accounts = await act(() => accountsApi.list(site.id))
  if (accounts) siteAccounts.value = accounts
}

const validate = (): string | null => {
  if (!form.value.kind) return '请选择站点类型'
  if (!form.value.name.trim()) return '请填写名称'
  if (!/^https?:\/\//.test(form.value.base_url.trim())) return '站点地址需以 http(s):// 开头'
  const missing = selectedProvider.value?.config_fields.find((field) => field.required && !(form.value.config[field.key] ?? '').trim())
  if (missing) return `请填写「${missing.label}」`
  return null
}

const submit = async () => {
  const problem = validate()
  if (problem) {
    message.warning(problem)
    return
  }
  const payload: SiteInput = { ...form.value, name: form.value.name.trim(), base_url: form.value.base_url.trim().replace(/\/+$/, '') }
  saving.value = true
  const id = editingId.value
  const saved = await act(() => (id ? sitesApi.update(id, payload) : sitesApi.create(payload)), id ? '已保存' : '已添加')
  saving.value = false
  if (!saved) return
  upsertSite(saved)
  drawerVisible.value = false
}

const minInterval = computed<number>(() => selectedProvider.value?.min_interval_sec || 1)

onMounted(() => {
  void run()
})
</script>

<template>
  <div class="page">
    <PageHeader title="站点" subtitle="被监控的商家站点及其轮询配置">
      <n-button :loading="loading" @click="run()">
        <template #icon><n-icon><RefreshOutline /></n-icon></template>
        刷新
      </n-button>
      <n-button type="primary" :disabled="providers.length === 0" @click="openCreate">
        <template #icon><n-icon><AddOutline /></n-icon></template>
        添加站点
      </n-button>
    </PageHeader>

    <n-spin :show="loading && sites.length === 0">
      <n-card v-if="sites.length === 0" size="small">
        <n-empty description="还没有站点，先添加一个" style="padding: 32px 0" />
      </n-card>
      <div v-else class="grid-cards">
        <n-card v-for="site in sites" :key="site.id" size="small">
          <template #header>
            <div class="title">
              <span>{{ site.name }}</span>
              <n-tag size="small" :bordered="false">{{ providerOf(site.kind)?.name ?? site.kind }}</n-tag>
            </div>
          </template>
          <template #header-extra>
            <n-switch :value="site.enabled" :loading="toggling === site.id" size="small" @update:value="(value: boolean) => toggleEnabled(site, value)" />
          </template>
          <div class="info">
            <a :href="site.base_url" target="_blank" rel="noopener noreferrer" class="link">{{ site.base_url }}</a>
            <n-space :size="6" align="center">
              <n-tag size="small" :type="site.fetch_mode === 'flaresolverr' ? 'info' : 'default'" :bordered="false">
                {{ fetchModeLabels[site.fetch_mode] ?? site.fetch_mode }}
              </n-tag>
              <n-text depth="3">每 {{ site.interval_sec }} 秒</n-text>
            </n-space>
            <div class="stats">
              <span><n-text depth="3">商品 </n-text>{{ site.product_count }}</span>
              <span><n-text depth="3">有货 </n-text><n-text :type="site.in_stock_count > 0 ? 'success' : 'default'" strong>{{ site.in_stock_count }}</n-text></span>
              <span><n-text depth="3">上次轮询 </n-text><TimeAgo :value="site.last_poll_at" /></span>
            </div>
            <n-text v-if="site.last_error" type="error" class="error">
              <n-ellipsis :line-clamp="2" :tooltip="{ width: 'trigger' }">
                {{ site.consecutive_fail > 1 ? `连续失败 ${site.consecutive_fail} 次：` : '' }}{{ site.last_error }}
              </n-ellipsis>
            </n-text>
          </div>
          <template #action>
            <n-space justify="end" :size="8">
              <n-button size="small" :loading="polling === site.id" @click="pollNow(site)">立即轮询</n-button>
              <n-button size="small" @click="openEdit(site)">编辑</n-button>
              <ConfirmButton
                text="删除"
                type="error"
                secondary
                confirm="删除站点会同时删除其账号、关注规则和商品快照，确定？"
                :action="() => removeSite(site)"
              />
            </n-space>
          </template>
        </n-card>
      </div>
    </n-spin>

    <n-drawer v-model:show="drawerVisible" width="min(560px, 100vw)">
      <n-drawer-content :title="editingId ? '编辑站点' : '添加站点'" closable>
        <n-form label-placement="top">
          <n-form-item label="类型" required>
            <n-select :value="form.kind" :options="providerOptions" :disabled="Boolean(editingId)" @update:value="onKindChange" />
          </n-form-item>
          <n-alert v-if="selectedProvider" type="info" :show-icon="false" class="provider">
            <div>{{ selectedProvider.description }}</div>
            <n-space :size="4" style="margin-top: 6px">
              <n-tag v-for="label in enabledCapabilities" :key="label" size="small" type="success" :bordered="false">{{ label }}</n-tag>
              <n-tag v-for="kind in selectedProvider.captcha ?? []" :key="kind" size="small" type="warning" :bordered="false">{{ kind }}</n-tag>
            </n-space>
          </n-alert>
          <n-form-item label="名称" required>
            <n-input v-model:value="form.name" placeholder="例如 NovixLink 诺联" />
          </n-form-item>
          <n-form-item label="站点地址" required>
            <n-input v-model:value="form.base_url" placeholder="https://example.com" />
          </n-form-item>
          <SchemaForm v-if="selectedProvider" v-model="form.config" :fields="selectedProvider.config_fields" />
          <n-form-item label="轮询间隔" :feedback="selectedProvider ? `最小 ${selectedProvider.min_interval_sec} 秒` : undefined">
            <n-input-number
              :value="form.interval_sec"
              :min="minInterval"
              style="width: 100%"
              @update:value="(value: number | null) => (form.interval_sec = value ?? selectedProvider?.default_interval_sec ?? DEFAULT_INTERVAL_SEC)"
            >
              <template #suffix>秒</template>
            </n-input-number>
          </n-form-item>
          <n-form-item label="抓取方式">
            <n-radio-group v-model:value="form.fetch_mode">
              <n-space vertical :size="4">
                <n-radio value="direct">直连</n-radio>
                <n-radio value="flaresolverr">FlareSolverr 浏览器代理（Cloudflare 站点）</n-radio>
              </n-space>
            </n-radio-group>
          </n-form-item>
          <n-form-item
            label="监控账号"
            :feedback="editingId ? '仅当库存需要登录才能看到时需要' : '保存站点并添加账号后再来选择；仅当库存需要登录才能看到时需要'"
          >
            <n-select
              :value="form.monitor_account_id || null"
              :options="accountOptions"
              :disabled="!editingId"
              clearable
              placeholder="不使用账号"
              @update:value="(value: string | null) => (form.monitor_account_id = value ?? '')"
            />
          </n-form-item>
          <n-form-item label="启用">
            <n-switch v-model:value="form.enabled" />
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
.title {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
}
.info {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.link {
  word-break: break-all;
}
.stats {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 16px;
}
.error {
  font-size: 12px;
}
.provider {
  margin-bottom: 16px;
  font-size: 13px;
}
</style>
