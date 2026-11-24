<script setup lang="ts">
// 站点新建 / 编辑抽屉：按 Provider 的配置字段渲染表单，含轮询间隔、抓取方式、出口优先级与监控账号
import { computed, ref, watch } from 'vue'
import {
  NAlert,
  NButton,
  NDrawer,
  NDrawerContent,
  NForm,
  NFormItem,
  NInput,
  NInputNumber,
  NRadio,
  NRadioGroup,
  NSelect,
  NSpace,
  NSwitch,
  NTag,
  NText,
  useMessage,
  type SelectOption,
} from 'naive-ui'
import { accountsApi, productsApi, sitesApi } from '@/api'
import type { Account, EgressProxy, ProviderMeta, Site, SiteInput } from '@/api/types'
import { useAction, useBusy } from '@/composables/useAsync'
import { useProductHiding } from '@/composables/useProductHiding'
import { toSiteInput, upsertSite, useProviders, useSites } from '@/composables/useSites'
import { egressOptions } from '@/utils/egress'
import { challengeKindMeta, metaOf } from '@/utils/labels'
import { URL_TEXT } from '@/utils/placeholders'
import QuickIntervalButtons from './QuickIntervalButtons.vue'
import SchemaForm from './SchemaForm.vue'

// Provider 未声明默认间隔时的兜底
const DEFAULT_INTERVAL_SEC = 60

const capabilityLabels: Record<keyof ProviderMeta['capabilities'], string> = {
  catalog: '商品目录',
  coupon: '优惠码',
  order: '自动下单',
  session_check: '会话校验',
  login: '账号密码登录',
  cancel: '取消订单',
  stock_needs_login: '库存需登录',
}

const props = defineProps<{ show: boolean; site?: Site | null; proxies: EgressProxy[] }>()
const emit = defineEmits<{ (e: 'update:show', value: boolean): void }>()

const message = useMessage()
const act = useAction()
const { isBusy, track } = useBusy()
const { providers, providerOf } = useProviders()
const form = ref<SiteInput>(emptyInput())
const siteAccounts = ref<Account[]>([])
const editingId = computed(() => props.site?.id ?? null)

// 已屏蔽商品：ID 取共享站点列表里的最新值，名称按需加载
const { sites } = useSites()
const { unhide } = useProductHiding()
const hiddenNames = ref<Record<string, string>>({})
const hiddenIds = computed<string[]>(() => sites.value.find((item) => item.id === editingId.value)?.hidden_products ?? props.site?.hidden_products ?? [])
const loadHiddenNames = async (siteId: string) => {
  const list = await productsApi.list({ site_id: siteId, include_hidden: 1 }).catch(() => [])
  hiddenNames.value = Object.fromEntries(list.filter((item) => item.hidden).map((item) => [item.id, item.name]))
}

function emptyInput(): SiteInput {
  return { kind: '', name: '', base_url: '', config: {}, interval_sec: DEFAULT_INTERVAL_SEC, fetch_mode: 'direct', egress: [], monitor_account_id: '', enabled: true }
}

const selectedProvider = computed<ProviderMeta | undefined>(() => providerOf(form.value.kind))
const minInterval = computed<number>(() => selectedProvider.value?.min_interval_sec || 1)
const providerOptions = computed<SelectOption[]>(() => providers.value.map((item) => ({ label: `${item.name}（${item.kind}）`, value: item.kind })))
const egressSelectOptions = computed(() => egressOptions(props.proxies))
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

const setIntervalSec = (value: number | null) => {
  if (value !== null) form.value.interval_sec = value
}

watch(
  () => props.show,
  async (visible) => {
    if (!visible) return
    siteAccounts.value = []
    const site = props.site
    if (!site) {
      form.value = emptyInput()
      const first = providers.value[0]
      if (first) onKindChange(first.kind)
      return
    }
    form.value = toSiteInput(site)
    hiddenNames.value = {}
    if (site.hidden_products?.length) void loadHiddenNames(site.id)
    const accounts = await act(() => accountsApi.list(site.id))
    if (accounts) siteAccounts.value = accounts
  },
)

const validate = (): string | null => {
  if (!form.value.kind) return '请选择站点类型'
  if (!form.value.name.trim()) return '请填写名称'
  if (!/^https?:\/\//.test(form.value.base_url.trim())) return '站点地址需以 http(s):// 开头'
  const missing = selectedProvider.value?.config_fields.find((field) => field.required && !(form.value.config[field.key] ?? '').trim())
  if (missing) return `请填写「${missing.label}」`
  return null
}

const close = () => emit('update:show', false)

const submit = async () => {
  const problem = validate()
  if (problem) {
    message.warning(problem)
    return
  }
  const payload: SiteInput = { ...form.value, name: form.value.name.trim(), base_url: form.value.base_url.trim().replace(/\/+$/, '') }
  const id = editingId.value
  const saved = await track('save', () =>
    act(() => (id ? sitesApi.update(id, payload) : sitesApi.create(payload)), id ? '已保存' : '已添加'),
  )
  if (!saved) return
  upsertSite(saved)
  close()
}
</script>

<template>
  <n-drawer :show="show" width="min(560px, 100vw)" @update:show="(value: boolean) => emit('update:show', value)">
    <n-drawer-content :title="editingId ? '编辑站点' : '添加站点'" closable>
      <n-form label-placement="top">
        <n-form-item label="类型" required>
          <n-select :value="form.kind" :options="providerOptions" :disabled="Boolean(editingId)" @update:value="onKindChange" />
        </n-form-item>
        <n-alert v-if="selectedProvider" type="info" :show-icon="false" class="provider">
          <div>{{ selectedProvider.description }}</div>
          <n-space :size="4" style="margin-top: 6px">
            <n-tag v-for="label in enabledCapabilities" :key="label" size="small" type="success" :bordered="false">{{ label }}</n-tag>
            <n-tag v-for="kind in selectedProvider.captcha ?? []" :key="kind" size="small" type="warning" :bordered="false">
              {{ metaOf(challengeKindMeta, kind).label }}
            </n-tag>
          </n-space>
        </n-alert>
        <n-form-item label="名称" required>
          <n-input v-model:value="form.name" placeholder="例如 NovixLink 诺联" />
        </n-form-item>
        <n-form-item label="站点地址" required>
          <n-input v-model:value="form.base_url" :placeholder="URL_TEXT.site" />
        </n-form-item>
        <SchemaForm v-if="selectedProvider" v-model="form.config" :fields="selectedProvider.config_fields" />
        <n-form-item label="轮询间隔" :feedback="selectedProvider ? `最小 ${selectedProvider.min_interval_sec} 秒` : undefined">
          <n-input-number :value="form.interval_sec" :min="minInterval" style="width: 100%" @update:value="setIntervalSec">
            <template #suffix>秒</template>
          </n-input-number>
          <QuickIntervalButtons
            :value="form.interval_sec"
            :min-sec="minInterval"
            size="small"
            style="margin-left: 8px; flex-shrink: 0"
            @select="(sec: number) => (form.interval_sec = sec)"
          />
        </n-form-item>
        <n-form-item label="抓取方式">
          <n-radio-group v-model:value="form.fetch_mode">
            <n-space vertical :size="4">
              <n-radio value="direct">直连</n-radio>
              <n-radio value="flaresolverr">FlareSolverr 浏览器代理（Cloudflare 站点）</n-radio>
            </n-space>
          </n-radio-group>
        </n-form-item>
        <n-form-item label="出口优先级" feedback="按选择顺序排优先级，连接失败或被限流时自动换下一个；留空用「设置 → 出口」的默认优先级">
          <n-select v-model:value="form.egress" multiple :options="egressSelectOptions" placeholder="使用默认出口" />
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
        <n-form-item v-if="editingId && hiddenIds.length" :label="`已屏蔽商品（${hiddenIds.length}）`">
          <div class="hidden-list">
            <div v-for="id in hiddenIds" :key="id" class="hidden-item">
              <n-text class="hidden-name">{{ hiddenNames[id] ?? id }}</n-text>
              <n-button size="tiny" @click="unhide(editingId, id, '已恢复')">恢复</n-button>
            </div>
          </div>
        </n-form-item>
      </n-form>
      <template #footer>
        <n-space justify="end">
          <n-button @click="close">取消</n-button>
          <n-button type="primary" :loading="isBusy('save')" @click="submit">保存</n-button>
        </n-space>
      </template>
    </n-drawer-content>
  </n-drawer>
</template>

<style scoped>
.hidden-list {
  display: flex;
  flex-direction: column;
  gap: 6px;
  width: 100%;
}
.hidden-item {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
}
.hidden-name {
  word-break: break-word;
}
.provider {
  margin-bottom: 16px;
  font-size: 13px;
}
</style>
