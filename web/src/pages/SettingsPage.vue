<script setup lang="ts">
// 设置页：通知渠道（Telegram / SMTP / Bark）、验证码求解、出口线路、轮询策略与通知事件；密码类字段 "******" 表示已设置且不修改
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { onBeforeRouteLeave } from 'vue-router'
import {
  NAlert,
  NButton,
  NCard,
  NCheckbox,
  NCheckboxGroup,
  NForm,
  NFormItem,
  NInput,
  NInputNumber,
  NSelect,
  NSpace,
  NSpin,
  NSwitch,
  NTag,
  NText,
} from 'naive-ui'
import { egressApi, settingsApi } from '@/api'
import type { EgressProxy, EgressTestResult, EventType, Settings } from '@/api/types'
import EgressRow from '@/components/EgressRow.vue'
import { useAction, useAsync } from '@/composables/useAsync'
import { eventLabels } from '@/utils/format'
import PageHeader from '@/components/PageHeader.vue'

const SECRET_MASK = '******'
const SECRET_PLACEHOLDER = '已设置（留空不修改）'
const notifyEventOrder: EventType[] = ['restock', 'order', 'challenge', 'account', 'price_change', 'new_product', 'sold_out', 'poll_error']
const notifyOptions: { value: EventType; label: string }[] = notifyEventOrder.map((item) => ({ value: item, label: eventLabels[item] }))
// 直连是内置出口（ID 为 direct），与登记的代理一样参与优先级排序与回退。
const DIRECT_EGRESS: EgressProxy = { id: 'direct', name: '直连', url: '', enabled: true }
// 模板里经 computed 引用（vue-tsc 会把本文件模板中的普通常量误当作 ref 检查）
const directEgress = computed<EgressProxy>(() => DIRECT_EGRESS)

// 密码类字段的表单值：已设置时输入框留空，提交时空值回传掩码
interface SecretInputs {
  botToken: string
  emailPassword: string
  barkURLs: string
  externalToken: string
}

const act = useAction()
const loaded = ref(false)
const form = ref<Settings>(emptySettings())
const secrets = ref<SecretInputs>({ botToken: '', emailPassword: '', barkURLs: '', externalToken: '' })
const masked = ref<Record<keyof SecretInputs, boolean>>({ botToken: false, emailPassword: false, barkURLs: false, externalToken: false })
const savedSnapshot = ref('')
const saving = ref(false)
const testing = ref(false)
const testResults = ref<[string, string][] | null>(null)

function emptySettings(): Settings {
  return {
    telegram: { enabled: false, bot_token: '', chat_id: '', api_base: '' },
    email: { enabled: false, host: '', port: 587, username: '', password: '', from: '', to: '', starttls: true },
    bark: { enabled: false, device_urls: '', group: '', sound: '', icon: '' },
    solver: { flaresolverr_url: '', external_url: '', external_token: '', manual_timeout_sec: 300 },
    polling: { idle_interval_sec: 3600, viewer_interval_sec: 300 },
    egress: { proxies: [], default: [] },
    notify_events: [],
  }
}

// 载入服务端设置；掩码字段在表单里显示为空 + 占位提示
const applySettings = (settings: Settings) => {
  masked.value = {
    botToken: settings.telegram.bot_token === SECRET_MASK,
    emailPassword: settings.email.password === SECRET_MASK,
    barkURLs: settings.bark.device_urls === SECRET_MASK,
    externalToken: settings.solver.external_token === SECRET_MASK,
  }
  secrets.value = {
    botToken: masked.value.botToken ? '' : settings.telegram.bot_token,
    emailPassword: masked.value.emailPassword ? '' : settings.email.password,
    barkURLs: masked.value.barkURLs ? '' : settings.bark.device_urls,
    externalToken: masked.value.externalToken ? '' : settings.solver.external_token,
  }
  form.value = JSON.parse(JSON.stringify(settings)) as Settings
  form.value.egress = { proxies: settings.egress?.proxies ?? [], default: settings.egress?.default ?? [] }
  form.value.polling = {
    idle_interval_sec: settings.polling?.idle_interval_sec || 3600,
    viewer_interval_sec: settings.polling?.viewer_interval_sec || 300,
  }
  savedSnapshot.value = JSON.stringify(buildPayload(form.value))
  loaded.value = true
}

const { loading, run } = useAsync(async () => {
  const settings = await settingsApi.get()
  applySettings(settings)
  return settings
})


const secretValue = (key: keyof SecretInputs): string => {
  const typed = secrets.value[key]
  if (typed === '' && masked.value[key]) return SECRET_MASK
  return typed
}

const buildPayload = (current: Settings): Settings => ({
  ...current,
  telegram: { ...current.telegram, bot_token: secretValue('botToken') },
  email: { ...current.email, password: secretValue('emailPassword') },
  bark: { ...current.bark, device_urls: secretValue('barkURLs') },
  solver: { ...current.solver, external_token: secretValue('externalToken') },
  notify_events: [...current.notify_events],
})

// 与上次载入 / 保存时的请求体比较，判断是否有未保存修改
const dirty = computed<boolean>(() => loaded.value && JSON.stringify(buildPayload(form.value)) !== savedSnapshot.value)

const save = async (): Promise<boolean> => {
  if (!loaded.value) return false
  saving.value = true
  const payload = buildPayload(form.value)
  const saved = await act(() => settingsApi.save(payload), '已保存')
  saving.value = false
  if (!saved) return false
  applySettings(saved)
  return true
}

const testNotify = async () => {
  if (dirty.value && !(await save())) return
  testing.value = true
  const result = await act(() => settingsApi.testNotify())
  testing.value = false
  if (result) testResults.value = Object.entries(result.results)
}

const resultType = (value: string): 'success' | 'default' | 'error' => {
  if (value === 'ok') return 'success'
  if (value === 'disabled' || value === '') return 'default'
  return 'error'
}
const channelLabels: Record<string, string> = { telegram: 'Telegram', email: '邮件', bark: 'Bark' }
const channelLabel = (channel: string): string => channelLabels[channel] ?? channel
const resultText = (value: string): string => (value === 'ok' ? '成功' : value === 'disabled' ? '未启用' : value)

// ---------- 出口 ----------
const egressOptions = computed(() => [
  { label: '直连（本服务器出口）', value: DIRECT_EGRESS.id },
  ...form.value.egress.proxies.filter((proxy) => proxy.id).map((proxy) => ({ label: proxy.name || proxy.id, value: proxy.id, disabled: !proxy.enabled })),
])
const egressTesting = ref<Record<string, boolean>>({})
const egressResults = ref<Record<string, EgressTestResult>>({})

const addEgress = () => {
  const id = `p${Math.random().toString(16).slice(2, 8)}`
  form.value.egress.proxies = [...form.value.egress.proxies, { id, name: '', url: '', enabled: true }]
}
const updateEgress = (value: EgressProxy) => {
  form.value.egress.proxies = form.value.egress.proxies.map((proxy) => (proxy.id === value.id ? value : proxy))
}
const removeEgress = (id: string) => {
  form.value.egress.proxies = form.value.egress.proxies.filter((proxy) => proxy.id !== id)
  form.value.egress.default = form.value.egress.default.filter((item) => item !== id)
}
// 测试前先保存，保证服务端用的是当前填写的地址
const testEgress = async (id: string) => {
  if (dirty.value && !(await save())) return
  egressTesting.value = { ...egressTesting.value, [id]: true }
  const result = await act(() => egressApi.test(id))
  egressTesting.value = { ...egressTesting.value, [id]: false }
  egressResults.value = { ...egressResults.value, [id]: result ?? { ok: false, error: '请求失败' } }
}

const notifyEvents = computed<string[]>({
  get: () => form.value.notify_events,
  set: (value) => {
    form.value.notify_events = value as EventType[]
  },
})

const secretPlaceholder = (key: keyof SecretInputs, fallback: string) => (masked.value[key] ? SECRET_PLACEHOLDER : fallback)

// 未保存离开提示：路由切换 + 刷新 / 关闭标签页
onBeforeRouteLeave(() => {
  if (!dirty.value) return true
  return window.confirm('设置尚未保存，确定离开？')
})
const beforeUnload = (event: BeforeUnloadEvent) => {
  if (!dirty.value) return
  event.preventDefault()
}
window.addEventListener('beforeunload', beforeUnload)
onBeforeUnmount(() => window.removeEventListener('beforeunload', beforeUnload))

onMounted(() => {
  void run()
})
</script>

<template>
  <div class="page">
    <PageHeader title="设置" subtitle="通知渠道、验证码求解与通知事件">
      <n-tag v-if="dirty" type="warning" size="small" :bordered="false" style="align-self: center">未保存</n-tag>
      <n-button type="primary" :loading="saving" :disabled="!loaded || !dirty" @click="save">保存</n-button>
    </PageHeader>
    <n-spin :show="loading && !loaded">
      <div v-if="loaded" class="cards">
        <n-card title="Telegram" size="small">
          <template #header-extra>
            <n-switch v-model:value="form.telegram.enabled" />
          </template>
          <n-form label-placement="top" size="small">
            <n-form-item label="Bot Token">
              <n-input
                v-model:value="secrets.botToken"
                type="password"
                show-password-on="click"
                :placeholder="secretPlaceholder('botToken', '123456:ABC-DEF...')"
              />
            </n-form-item>
            <n-form-item label="Chat ID" feedback="个人或群组 ID，可通过 @userinfobot 获取">
              <n-input v-model:value="form.telegram.chat_id" placeholder="123456789" />
            </n-form-item>
            <n-form-item label="API 地址（可选）" feedback="国内服务器可填反代地址">
              <n-input v-model:value="form.telegram.api_base" placeholder="https://api.telegram.org" />
            </n-form-item>
          </n-form>
        </n-card>

        <n-card title="邮件 SMTP" size="small">
          <template #header-extra>
            <n-switch v-model:value="form.email.enabled" />
          </template>
          <n-form label-placement="top" size="small">
            <div class="row-2">
              <n-form-item label="服务器">
                <n-input v-model:value="form.email.host" placeholder="smtp.gmail.com" />
              </n-form-item>
              <n-form-item label="端口">
                <n-input-number
                  :value="form.email.port"
                  :min="1"
                  :max="65535"
                  :show-button="false"
                  style="width: 100%"
                  @update:value="(value: number | null) => (form.email.port = value ?? 587)"
                />
              </n-form-item>
            </div>
            <n-form-item label="STARTTLS" feedback="465 端口自动使用 TLS">
              <n-switch v-model:value="form.email.starttls" />
            </n-form-item>
            <n-form-item label="用户名">
              <n-input v-model:value="form.email.username" placeholder="me@example.com" />
            </n-form-item>
            <n-form-item label="密码">
              <n-input
                v-model:value="secrets.emailPassword"
                type="password"
                show-password-on="click"
                :placeholder="secretPlaceholder('emailPassword', '授权码 / 应用专用密码')"
              />
            </n-form-item>
            <n-form-item label="发件人">
              <n-input v-model:value="form.email.from" placeholder="restockd <me@example.com>" />
            </n-form-item>
            <n-form-item label="收件人" feedback="多个用逗号分隔">
              <n-input v-model:value="form.email.to" placeholder="a@example.com, b@example.com" />
            </n-form-item>
          </n-form>
        </n-card>

        <n-card title="Bark（iOS 推送）" size="small">
          <template #header-extra>
            <n-switch v-model:value="form.bark.enabled" />
          </template>
          <n-form label-placement="top" size="small">
            <n-form-item label="设备地址" feedback="Bark App 里复制的 https://<服务器>/<设备 Key>，多台设备用逗号或换行分隔">
              <n-input
                v-model:value="secrets.barkURLs"
                type="textarea"
                :autosize="{ minRows: 1, maxRows: 4 }"
                :placeholder="secretPlaceholder('barkURLs', 'https://bark.example.com/xxxxxxxx')"
              />
            </n-form-item>
            <div class="row-2">
              <n-form-item label="分组">
                <n-input v-model:value="form.bark.group" placeholder="restockd" />
              </n-form-item>
              <n-form-item label="铃声">
                <n-input v-model:value="form.bark.sound" placeholder="默认" />
              </n-form-item>
            </div>
            <n-form-item label="图标 URL（可选）" feedback="补货 / 锁单 / 人工验证为「时效性」通知，可突破专注模式">
              <n-input v-model:value="form.bark.icon" placeholder="https://example.com/icon.png" />
            </n-form-item>
          </n-form>
        </n-card>

        <n-card title="验证码 / 反爬" size="small">
          <n-form label-placement="top" size="small">
            <n-form-item label="FlareSolverr 地址" feedback="docker run -p 8191:8191 ghcr.io/flaresolverr/flaresolverr">
              <n-input v-model:value="form.solver.flaresolverr_url" placeholder="http://127.0.0.1:8191" />
            </n-form-item>
            <n-form-item label="外部求解服务 URL（可选）">
              <n-input v-model:value="form.solver.external_url" placeholder="https://solver.example.com/solve" />
            </n-form-item>
            <n-form-item label="外部求解服务 Token">
              <n-input
                v-model:value="secrets.externalToken"
                type="password"
                show-password-on="click"
                :placeholder="secretPlaceholder('externalToken', '以 Authorization: Bearer 发送')"
              />
            </n-form-item>
            <n-alert type="default" :show-icon="false" class="contract">
              外部服务约定：<code>POST &lt;URL&gt;</code>，请求
              <code>{"kind","site_key","page_url","action"}</code>，返回
              <code>{"token","cookies","user_agent"}</code>，失败返回 <code>{"error"}</code>。用于 Turnstile / reCAPTCHA / hCaptcha 等 token 类验证。
            </n-alert>
            <n-form-item label="人工验证超时" feedback="自动求解失败后在「人工验证」等待的时长">
              <n-input-number
                :value="form.solver.manual_timeout_sec"
                :min="30"
                :step="30"
                style="width: 100%"
                @update:value="(value: number | null) => (form.solver.manual_timeout_sec = value ?? 300)"
              >
                <template #suffix>秒</template>
              </n-input-number>
            </n-form-item>
          </n-form>
        </n-card>

        <n-card title="出口" size="small">
          <n-alert type="default" :show-icon="false" class="contract">
            站点按优先级使用出口（直连也是一个出口）：连接失败或被站点限流（429）时自动换下一个，并暂时冷却出问题的出口
            （限流只对被限的页面生效）。下单等非幂等请求只在连接未建立时换出口，避免重复提交。站点可在「站点 → 出口优先级」单独指定。
          </n-alert>
          <div class="proxies">
            <EgressRow
              :egress="directEgress"
              builtin
              :testing="egressTesting[directEgress.id]"
              :result="egressResults[directEgress.id]"
              @test="testEgress(directEgress.id)"
            />
            <EgressRow
              v-for="item in form.egress.proxies"
              :key="item.id"
              :egress="item"
              :testing="egressTesting[item.id]"
              :result="egressResults[item.id]"
              @update="updateEgress"
              @test="testEgress(item.id)"
              @remove="removeEgress(item.id)"
            />
            <n-space :size="8">
              <n-button size="small" dashed @click="addEgress">添加出口（代理）</n-button>
            </n-space>
          </div>
          <n-form label-placement="top" size="small" style="margin-top: 12px">
            <n-form-item label="默认出口优先级" feedback="未单独指定出口的站点使用；按选择顺序排优先级，留空即只用直连">
              <n-select v-model:value="form.egress.default" multiple :options="egressOptions" placeholder="直连" />
            </n-form-item>
          </n-form>
        </n-card>

        <n-card title="轮询策略" size="small">
          <n-form label-placement="top" size="small">
            <n-alert type="default" :show-icon="false" class="contract">
              有启用中的关注规则的站点按站点自身间隔轮询；没有关注的站点自动降频，有人打开管理界面时立即刷新一次并提速。
              降频间隔不会低于站点自身设置。
            </n-alert>
            <div class="row-2 even">
              <n-form-item label="无人访问时">
                <n-input-number
                  :value="Math.round(form.polling.idle_interval_sec / 60)"
                  :min="1"
                  style="width: 100%"
                  @update:value="(value: number | null) => (form.polling.idle_interval_sec = (value ?? 60) * 60)"
                >
                  <template #suffix>分钟</template>
                </n-input-number>
              </n-form-item>
              <n-form-item label="有人访问时">
                <n-input-number
                  :value="Math.round(form.polling.viewer_interval_sec / 60)"
                  :min="1"
                  style="width: 100%"
                  @update:value="(value: number | null) => (form.polling.viewer_interval_sec = (value ?? 5) * 60)"
                >
                  <template #suffix>分钟</template>
                </n-input-number>
              </n-form-item>
            </div>
          </n-form>
        </n-card>

        <n-card title="通知事件" size="small">
          <n-checkbox-group v-model:value="notifyEvents">
            <n-space item-style="display: flex">
              <n-checkbox v-for="option in notifyOptions" :key="option.value" :value="option.value" :label="option.label" />
            </n-space>
          </n-checkbox-group>
          <div class="test">
            <n-button :loading="testing" @click="testNotify">发送测试通知</n-button>
            <n-text v-if="dirty" depth="3" style="font-size: 12px">将先保存当前修改</n-text>
          </div>
          <n-space v-if="testResults" :size="8" style="margin-top: 10px">
            <n-tag v-for="entry in testResults" :key="entry[0]" :type="resultType(entry[1])" size="small" :bordered="false">
              {{ channelLabel(entry[0]) }}：{{ resultText(entry[1]) }}
            </n-tag>
          </n-space>
        </n-card>
      </div>
      <div v-else class="placeholder" />
    </n-spin>
  </div>
</template>

<style scoped>
.cards {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(min(420px, 100%), 1fr));
  gap: 14px;
  align-items: start;
}
.row-2 {
  display: grid;
  grid-template-columns: 1fr 120px;
  gap: 0 12px;
}
.proxies {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.row-2.even {
  grid-template-columns: 1fr 1fr;
}
.contract {
  margin: -4px 0 16px;
  font-size: 12px;
  word-break: break-word;
}
.test {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 10px;
  margin-top: 16px;
}
.placeholder {
  min-height: 200px;
}
</style>
