<script setup lang="ts">
// 人工验证收件箱：自动求解失败时在此手动提交 token / Cookie，实时增删并显示倒计时
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import {
  NButton,
  NCard,
  NCollapse,
  NCollapseItem,
  NEmpty,
  NForm,
  NFormItem,
  NIcon,
  NInput,
  NSpace,
  NSpin,
  NTag,
  NText,
  useMessage,
} from 'naive-ui'
import { CopyOutline, RefreshOutline } from '@vicons/ionicons5'
import { challengesApi, sitesApi } from '@/api'
import type { ChallengeKind, PendingChallenge, Site } from '@/api/types'
import { useAction, useAsync } from '@/composables/useAsync'
import { onLive } from '@/composables/useLive'
import { formatDateTime } from '@/utils/format'
import PageHeader from '@/components/PageHeader.vue'
import ConfirmButton from '@/components/ConfirmButton.vue'

interface SolveForm {
  token: string
  cookies: string
  userAgent: string
}

const URGENT_SECONDS = 60

const kindLabels: Record<ChallengeKind, string> = {
  cloudflare: 'Cloudflare 质询',
  turnstile: 'Turnstile',
  recaptcha_v2: 'reCAPTCHA v2',
  recaptcha_v3: 'reCAPTCHA v3',
  hcaptcha: 'hCaptcha',
}

const kindHelp: Record<ChallengeKind, string> = {
  cloudflare: '在浏览器打开页面通过 Cloudflare 验证后，粘贴含 cf_clearance 的 Cookie 和获取时使用的 UA（需与本服务器同一出口 IP 才有效）。',
  turnstile: '在目标站点页面完成验证后，从开发者工具复制 cf-turnstile-response 的值。',
  recaptcha_v2: '在目标站点页面勾选验证后，从开发者工具复制 g-recaptcha-response 的值。',
  recaptcha_v3: '在目标站点页面触发操作后，从开发者工具复制 g-recaptcha-response（或 grecaptcha.execute 返回）的 token。',
  hcaptcha: '在目标站点页面完成验证后，从开发者工具复制 h-captcha-response 的值。',
}

const message = useMessage()
const act = useAction()
const challenges = ref<PendingChallenge[]>([])
const sites = ref<Site[]>([])
const siteNames = computed<Record<string, string>>(() => Object.fromEntries(sites.value.map((site) => [site.id, site.name])))
const forms = reactive<Record<string, SolveForm>>({})
const submitting = ref<string | null>(null)
const now = ref(Date.now())
const timer = setInterval(() => {
  now.value = Date.now()
}, 1000)
onBeforeUnmount(() => clearInterval(timer))

// 列表变化时同步为新挑战创建表单（同步执行，保证渲染前已存在）
watch(
  challenges,
  (list) => {
    for (const item of list) {
      if (!forms[item.id]) forms[item.id] = { token: '', cookies: '', userAgent: '' }
    }
  },
  { immediate: true, flush: 'sync' },
)
const emptyForm: SolveForm = { token: '', cookies: '', userAgent: '' }
const formOf = (id: string): SolveForm => forms[id] ?? emptyForm

const { loading, run } = useAsync(async () => {
  const [list, siteList] = await Promise.all([challengesApi.list(), sitesApi.list().catch(() => [] as Site[])])
  challenges.value = list
  sites.value = siteList
  return list
})

const removeLocal = (id: string) => {
  challenges.value = challenges.value.filter((item) => item.id !== id)
  delete forms[id]
}

onLive('challenge', (pending) => {
  if (challenges.value.some((item) => item.id === pending.id)) return
  challenges.value = [pending, ...challenges.value]
})
onLive('challenge_done', ({ id }) => removeLocal(id))

const isCookieKind = (kind: ChallengeKind) => kind === 'cloudflare'

// 剩余秒数（未设置过期时间返回 null）
const remainingSeconds = (pending: PendingChallenge): number | null => {
  const expires = new Date(pending.expires_at).getTime()
  if (Number.isNaN(expires) || expires <= 0) return null
  return Math.max(0, Math.floor((expires - now.value) / 1000))
}

const countdownText = (seconds: number | null): string => {
  if (seconds === null) return '-'
  const minutes = Math.floor(seconds / 60)
  const rest = seconds % 60
  return `${String(minutes).padStart(2, '0')}:${String(rest).padStart(2, '0')}`
}

const copy = async (text: string) => {
  try {
    await navigator.clipboard.writeText(text)
    message.success('已复制')
  } catch {
    message.warning('复制失败，请手动选择复制')
  }
}

const fillBrowserUA = (id: string) => {
  formOf(id).userAgent = navigator.userAgent
}

const canSubmit = (id: string): boolean => {
  const form = formOf(id)
  return Boolean(form.token.trim() || form.cookies.trim())
}

const submit = async (pending: PendingChallenge) => {
  const form = formOf(pending.id)
  if (!canSubmit(pending.id)) {
    message.warning('请至少填写 token 或 Cookie')
    return
  }
  submitting.value = pending.id
  const result = await act(
    () =>
      challengesApi.solve(pending.id, {
        token: form.token.trim() || undefined,
        cookies_raw: form.cookies.trim() || undefined,
        user_agent: form.userAgent.trim() || undefined,
      }),
    '已提交，任务继续执行',
  )
  submitting.value = null
  if (result) removeLocal(pending.id)
}

const cancel = async (pending: PendingChallenge) => {
  const result = await act(() => challengesApi.cancel(pending.id), '已放弃')
  if (result) removeLocal(pending.id)
}

onMounted(() => {
  void run()
})
</script>

<template>
  <div class="page">
    <PageHeader title="人工验证" subtitle="自动求解器（FlareSolverr / 外部服务）失败时的兜底收件箱，请在过期前提交">
      <n-button :loading="loading" @click="run()">
        <template #icon><n-icon><RefreshOutline /></n-icon></template>
        刷新
      </n-button>
    </PageHeader>
    <n-spin :show="loading && challenges.length === 0">
      <n-card v-if="challenges.length === 0" size="small">
        <n-empty description="当前没有待处理的验证" style="padding: 32px 0" />
      </n-card>
      <div v-else class="list">
        <n-card v-for="pending in challenges" :key="pending.id" size="small">
          <template #header>
            <n-space align="center" :size="8" :wrap="true">
              <n-tag type="warning" size="small" :bordered="false">{{ kindLabels[pending.challenge.kind] ?? pending.challenge.kind }}</n-tag>
              <span class="purpose">{{ pending.challenge.purpose || '待验证' }}</span>
            </n-space>
          </template>
          <template #header-extra>
            <n-text
              :type="(remainingSeconds(pending) ?? Infinity) < URGENT_SECONDS ? 'error' : 'default'"
              strong
              class="countdown"
              :title="`过期时间 ${formatDateTime(pending.expires_at)}`"
            >
              {{ countdownText(remainingSeconds(pending)) }}
            </n-text>
          </template>
          <div class="meta">
            <div class="meta-row">
              <n-text depth="3">站点</n-text>
              <span>{{ siteNames[pending.challenge.site_id] ?? pending.challenge.site_id }}</span>
            </div>
            <div class="meta-row">
              <n-text depth="3">页面</n-text>
              <a :href="pending.challenge.page_url" target="_blank" rel="noopener noreferrer" class="link">{{ pending.challenge.page_url }}</a>
            </div>
            <div v-if="pending.challenge.site_key" class="meta-row">
              <n-text depth="3">site_key</n-text>
              <span class="key">
                <code>{{ pending.challenge.site_key }}</code>
                <n-button text size="tiny" @click="copy(pending.challenge.site_key ?? '')">
                  <template #icon><n-icon><CopyOutline /></n-icon></template>
                </n-button>
              </span>
            </div>
            <div v-if="pending.challenge.action" class="meta-row">
              <n-text depth="3">action</n-text>
              <code>{{ pending.challenge.action }}</code>
            </div>
            <div class="meta-row">
              <n-text depth="3">时间</n-text>
              <span>{{ formatDateTime(pending.created_at) }} → {{ formatDateTime(pending.expires_at) }}</span>
            </div>
          </div>
          <n-text depth="3" class="help">{{ kindHelp[pending.challenge.kind] }}</n-text>
          <n-form label-placement="top" size="small" class="solve-form">
            <template v-if="isCookieKind(pending.challenge.kind)">
              <n-form-item label="Cookie">
                <n-input v-model:value="formOf(pending.id).cookies" type="textarea" :autosize="{ minRows: 3, maxRows: 8 }" placeholder="cf_clearance=...; ..." />
              </n-form-item>
              <n-form-item label="User-Agent">
                <n-input v-model:value="formOf(pending.id).userAgent" placeholder="获取 Cookie 时的浏览器 UA">
                  <template #suffix>
                    <n-button text size="tiny" @click="fillBrowserUA(pending.id)">用当前浏览器</n-button>
                  </template>
                </n-input>
              </n-form-item>
            </template>
            <n-form-item v-else label="Token">
              <n-input v-model:value="formOf(pending.id).token" type="textarea" :autosize="{ minRows: 3, maxRows: 8 }" placeholder="粘贴验证 token" />
            </n-form-item>
            <n-collapse>
              <n-collapse-item title="更多" :name="`more-${pending.id}`">
                <template v-if="isCookieKind(pending.challenge.kind)">
                  <n-form-item label="Token">
                    <n-input v-model:value="formOf(pending.id).token" type="textarea" :autosize="{ minRows: 2, maxRows: 6 }" placeholder="可选" />
                  </n-form-item>
                </template>
                <template v-else>
                  <n-form-item label="Cookie">
                    <n-input v-model:value="formOf(pending.id).cookies" type="textarea" :autosize="{ minRows: 2, maxRows: 6 }" placeholder="可选" />
                  </n-form-item>
                  <n-form-item label="User-Agent">
                    <n-input v-model:value="formOf(pending.id).userAgent" placeholder="可选">
                      <template #suffix>
                        <n-button text size="tiny" @click="fillBrowserUA(pending.id)">用当前浏览器</n-button>
                      </template>
                    </n-input>
                  </n-form-item>
                </template>
              </n-collapse-item>
            </n-collapse>
          </n-form>
          <template #action>
            <n-space justify="end">
              <ConfirmButton text="放弃" confirm="放弃后对应任务将失败，确定？" :action="() => cancel(pending)" />
              <n-button
                type="primary"
                size="small"
                :disabled="!canSubmit(pending.id)"
                :loading="submitting === pending.id"
                @click="submit(pending)"
              >
                提交
              </n-button>
            </n-space>
          </template>
        </n-card>
      </div>
    </n-spin>
  </div>
</template>

<style scoped>
.list {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(min(420px, 100%), 1fr));
  gap: 14px;
}
.purpose {
  word-break: break-word;
}
.countdown {
  font-variant-numeric: tabular-nums;
  font-size: 16px;
}
.meta {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-bottom: 10px;
}
.meta-row {
  display: grid;
  grid-template-columns: 64px 1fr;
  gap: 8px;
  align-items: start;
  min-width: 0;
}
.link,
.key code {
  word-break: break-all;
}
.key {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  min-width: 0;
}
.help {
  display: block;
  font-size: 12px;
  margin-bottom: 10px;
}
.solve-form {
  margin-top: 4px;
}
</style>
