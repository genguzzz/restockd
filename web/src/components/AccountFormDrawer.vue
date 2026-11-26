<script setup lang="ts">
// 账号新建 / 编辑抽屉：站点、备注名、登录凭据（站点支持时）、UA、默认下单参数、初始 Cookie
import { computed, ref, watch } from 'vue'
import { NButton, NDrawer, NDrawerContent, NForm, NFormItem, NInput, NSelect, NSpace, NText, useMessage } from 'naive-ui'
import { accountsApi, SECRET_MASK } from '@/api'
import type { Account, AccountInput, OrderDefaults } from '@/api/types'
import { useAction, useBusy } from '@/composables/useAsync'
import { useProviders, useSites } from '@/composables/useSites'
import SecretInput from './SecretInput.vue'
import UserAgentInput from './UserAgentInput.vue'

const props = defineProps<{ show: boolean; account?: Account | null }>()
const emit = defineEmits<{
  (e: 'update:show', value: boolean): void
  (e: 'saved', account: Account): void
}>()

interface AccountForm {
  site_id: string | null
  label: string
  user_agent: string
  defaults: Required<OrderDefaults>
  cookies_raw: string
  username: string
  /** "******" 保留已保存的密码、空串清除、其他为新密码 */
  password: string
}

const message = useMessage()
const act = useAction()
const { isBusy, track } = useBusy()
const { sites, siteOptions } = useSites()
const { providerOf } = useProviders()
const form = ref<AccountForm>(fromAccount(null))
const editingId = computed(() => props.account?.id ?? null)
const canLogin = computed(() => {
  const site = sites.value.find((item) => item.id === form.value.site_id)
  return Boolean(site && providerOf(site.kind)?.capabilities.login)
})

function fromAccount(account: Account | null): AccountForm {
  return {
    site_id: account?.site_id ?? sites.value[0]?.id ?? null,
    label: account?.label ?? '',
    user_agent: account?.user_agent ?? '',
    defaults: {
      gateway: account?.defaults?.gateway ?? '',
      os: account?.defaults?.os ?? '',
      hostname: account?.defaults?.hostname ?? '',
      ssh_key: account?.defaults?.ssh_key ?? '',
      coupon: account?.defaults?.coupon ?? '',
    },
    cookies_raw: '',
    username: account?.username ?? '',
    password: account?.has_password ? SECRET_MASK : '',
  }
}

watch(
  () => props.show,
  (visible) => {
    if (visible) form.value = fromAccount(props.account ?? null)
  },
)

const close = () => emit('update:show', false)

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
  }
  // 站点不支持账号密码登录时不提交凭据（省略即不修改）
  if (canLogin.value) {
    input.username = current.username.trim()
    input.password = current.password
  }
  const id = editingId.value
  if (!id && current.cookies_raw.trim()) input.cookies_raw = current.cookies_raw.trim()
  const saved = await track('save', () =>
    act(() => (id ? accountsApi.update(id, input) : accountsApi.create(input)), id ? '已保存' : '已添加'),
  )
  if (!saved) return
  emit('saved', saved)
  close()
}
</script>

<template>
  <n-drawer :show="show" width="min(560px, 100vw)" @update:show="(value: boolean) => emit('update:show', value)">
    <n-drawer-content :title="editingId ? '编辑账号' : '添加账号'" closable>
      <n-form label-placement="top">
        <n-form-item label="站点" required>
          <n-select v-model:value="form.site_id" :options="siteOptions" :disabled="Boolean(editingId)" placeholder="选择站点" />
        </n-form-item>
        <n-form-item label="备注名" required>
          <n-input v-model:value="form.label" placeholder="例如 主号" />
        </n-form-item>
        <template v-if="canLogin">
          <n-text depth="3" class="section">登录凭据（托管后会话失效自动登录）</n-text>
          <div class="row-2">
            <n-form-item label="用户名 / 邮箱">
              <n-input v-model:value="form.username" placeholder="me@example.com" :input-props="{ autocomplete: 'off' }" />
            </n-form-item>
            <n-form-item label="密码">
              <SecretInput v-model:value="form.password" placeholder="可选" />
            </n-form-item>
          </div>
        </template>
        <n-form-item label="User-Agent" feedback="使用 cf_clearance 时必须与获取 Cookie 的浏览器一致">
          <UserAgentInput v-model:value="form.user_agent" placeholder="留空使用默认 UA" />
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
        <div class="row-2">
          <n-form-item label="主机名">
            <n-input v-model:value="form.defaults.hostname" placeholder="可选" />
          </n-form-item>
          <n-form-item label="默认优惠码">
            <n-input v-model:value="form.defaults.coupon" placeholder="下单未填写时自动使用" />
          </n-form-item>
        </div>
        <n-form-item label="SSH 公钥">
          <n-input v-model:value="form.defaults.ssh_key" type="textarea" :autosize="{ minRows: 2, maxRows: 6 }" placeholder="ssh-ed25519 AAAA..." />
        </n-form-item>
        <n-form-item v-if="!editingId" label="Cookie（可选）" feedback="支持 a=1; b=2、Cookie-Editor JSON、cookies.txt；也可稍后用「粘贴 Cookie」设置">
          <n-input v-model:value="form.cookies_raw" type="textarea" :autosize="{ minRows: 3, maxRows: 8 }" placeholder="laravel_session=...; XSRF-TOKEN=..." />
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
