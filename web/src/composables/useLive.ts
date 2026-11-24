// 全局唯一的实时连接：布局层启动一次，页面通过 onLive 订阅，离开页面自动取消
import { effectScope, onScopeDispose, ref, watch } from 'vue'
import { challengesApi } from '@/api'
import type { StreamEvents } from '@/api/types'
import { createStream, type StreamController } from './useSSE'

type Listener<K extends keyof StreamEvents> = (data: StreamEvents[K]) => void

const listeners: { [K in keyof StreamEvents]: Set<Listener<K>> } = {
  event: new Set(),
  site: new Set(),
  challenge: new Set(),
  challenge_done: new Set(),
  order: new Set(),
}

export const liveConnected = ref(false)
export const pendingChallengeIds = ref<string[]>([])

let controller: StreamController | null = null

function emit<K extends keyof StreamEvents>(name: K, data: StreamEvents[K]): void {
  for (const listener of listeners[name]) listener(data)
}

export async function startLive(): Promise<void> {
  if (controller) return
  controller = createStream({
    event: (data) => emit('event', data),
    site: (data) => emit('site', data),
    order: (data) => emit('order', data),
    challenge: (data) => {
      if (!pendingChallengeIds.value.includes(data.id)) pendingChallengeIds.value = [...pendingChallengeIds.value, data.id]
      emit('challenge', data)
    },
    challenge_done: (data) => {
      pendingChallengeIds.value = pendingChallengeIds.value.filter((id) => id !== data.id)
      emit('challenge_done', data)
    },
  })
  // connected 状态同步到全局（独立作用域，不随组件销毁）
  const current = controller
  effectScope(true).run(() => {
    watch(current.connected, (value) => {
      liveConnected.value = value
    }, { immediate: true })
  })
  try {
    const pending = await challengesApi.list()
    pendingChallengeIds.value = pending.map((item) => item.id)
  } catch {
    // 未登录时忽略
  }
}

export function stopLive(): void {
  controller?.close()
  controller = null
  liveConnected.value = false
}

export function onLive<K extends keyof StreamEvents>(name: K, listener: Listener<K>): void {
  listeners[name].add(listener)
  onScopeDispose(() => {
    listeners[name].delete(listener)
  })
}
