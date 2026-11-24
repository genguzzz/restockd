// SSE 订阅：断线指数退避重连，按事件名分发类型化数据
import { ref, type Ref } from 'vue'
import type { StreamEvents } from '@/api/types'

export type StreamHandlers = { [K in keyof StreamEvents]?: (data: StreamEvents[K]) => void }

export interface EventSourceLike {
  onopen: ((event: Event) => void) | null
  onerror: ((event: Event) => void) | null
  addEventListener(type: string, listener: (event: MessageEvent) => void): void
  close(): void
}

export type EventSourceFactory = (url: string) => EventSourceLike

export interface StreamOptions {
  url?: string
  factory?: EventSourceFactory
  baseDelay?: number
  maxDelay?: number
  schedule?: (fn: () => void, ms: number) => unknown
  cancel?: (handle: unknown) => void
}

export interface StreamController {
  connected: Ref<boolean>
  attempts: Ref<number>
  close: () => void
}

// 第 n 次重连的等待时间：base * 2^n，封顶 max
export function backoffDelay(attempt: number, base: number, max: number): number {
  return Math.min(max, base * 2 ** attempt)
}

// 订阅的事件名：用 Record 保证与 StreamEvents 的键一一对应（漏写或多写都会编译报错）
const streamEventKeys: Record<keyof StreamEvents, true> = {
  event: true,
  site: true,
  challenge: true,
  challenge_done: true,
  order: true,
  account: true,
}
export const streamEventNames = Object.keys(streamEventKeys) as (keyof StreamEvents)[]

export function createStream(handlers: StreamHandlers, options: StreamOptions = {}): StreamController {
  const url = options.url ?? '/api/stream'
  const factory: EventSourceFactory =
    options.factory ?? ((target) => new EventSource(target, { withCredentials: true }) as unknown as EventSourceLike)
  const base = options.baseDelay ?? 1000
  const max = options.maxDelay ?? 30000
  const schedule = options.schedule ?? ((fn, ms) => setTimeout(fn, ms))
  const cancel = options.cancel ?? ((handle) => clearTimeout(handle as ReturnType<typeof setTimeout>))
  const connected = ref(false)
  const attempts = ref(0)
  let source: EventSourceLike | null = null
  let timer: unknown = null
  let closed = false

  const connect = () => {
    if (closed) return
    source = factory(url)
    source.onopen = () => {
      connected.value = true
      attempts.value = 0
    }
    source.onerror = () => {
      connected.value = false
      source?.close()
      source = null
      if (closed) return
      const delay = backoffDelay(attempts.value, base, max)
      attempts.value += 1
      timer = schedule(connect, delay)
    }
    for (const name of streamEventNames) {
      const handler = handlers[name] as ((data: unknown) => void) | undefined
      if (!handler) continue
      source.addEventListener(name, (event: MessageEvent) => {
        try {
          handler(JSON.parse(event.data))
        } catch {
          // 忽略无法解析的消息
        }
      })
    }
  }

  connect()
  return {
    connected,
    attempts,
    close: () => {
      closed = true
      if (timer !== null) cancel(timer)
      source?.close()
      source = null
      connected.value = false
    },
  }
}
