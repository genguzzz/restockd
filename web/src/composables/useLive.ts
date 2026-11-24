// 全局唯一的实时连接：布局层启动一次，页面通过 onLive 订阅，离开页面自动取消
import { effectScope, onScopeDispose, ref, watch, type EffectScope } from 'vue'
import { challengesApi } from '@/api'
import type { StreamEvents } from '@/api/types'
import { createStream, streamEventNames, type StreamController, type StreamHandlers } from './useSSE'

type Listener<K extends keyof StreamEvents> = (data: StreamEvents[K]) => void
type Listeners = { [K in keyof StreamEvents]: Set<Listener<K>> }

const listeners = Object.fromEntries(streamEventNames.map((name) => [name, new Set()])) as unknown as Listeners

export const liveConnected = ref(false)
export const pendingChallengeIds = ref<string[]>([])

let controller: StreamController | null = null
let scope: EffectScope | null = null

// 不随组件销毁的订阅（全局状态用），返回取消函数
export function subscribe<K extends keyof StreamEvents>(name: K, listener: Listener<K>): () => void {
  listeners[name].add(listener)
  return () => listeners[name].delete(listener)
}

// 组件内订阅：作用域销毁时自动取消
export function onLive<K extends keyof StreamEvents>(name: K, listener: Listener<K>): void {
  onScopeDispose(subscribe(name, listener))
}

subscribe('challenge', ({ id }) => {
  if (!pendingChallengeIds.value.includes(id)) pendingChallengeIds.value = [...pendingChallengeIds.value, id]
})
subscribe('challenge_done', ({ id }) => {
  pendingChallengeIds.value = pendingChallengeIds.value.filter((item) => item !== id)
})

// 每次连上（含断线重连）都重新拉取待处理验证，补上断线期间错过的增删
async function refreshPending(): Promise<void> {
  try {
    pendingChallengeIds.value = (await challengesApi.list()).map((item) => item.id)
  } catch {
    // 未登录时忽略
  }
}

export function startLive(): void {
  if (controller) return
  const handlers: StreamHandlers = {}
  for (const name of streamEventNames) {
    ;(handlers as Record<string, (data: unknown) => void>)[name] = (data) => {
      for (const listener of listeners[name] as Set<(data: unknown) => void>) listener(data)
    }
  }
  const current = createStream(handlers)
  controller = current
  // connected 状态同步到全局（独立作用域，不随组件销毁）
  scope = effectScope(true)
  scope.run(() => {
    watch(current.connected, (value) => {
      liveConnected.value = value
      if (value) void refreshPending()
    }, { immediate: true })
  })
}

export function stopLive(): void {
  controller?.close()
  controller = null
  scope?.stop()
  scope = null
  liveConnected.value = false
}
