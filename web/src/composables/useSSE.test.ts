// useSSE.ts 单元测试：假 EventSource + 假调度器，不依赖真实定时器
import { describe, expect, it, vi } from 'vitest'
import { backoffDelay, createStream, type EventSourceLike } from './useSSE'

class FakeSource implements EventSourceLike {
  onopen: ((event: Event) => void) | null = null
  onerror: ((event: Event) => void) | null = null
  closed = false
  readonly listeners = new Map<string, ((event: MessageEvent) => void)[]>()

  constructor(readonly url: string) {}

  addEventListener(type: string, listener: (event: MessageEvent) => void): void {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener])
  }

  close(): void {
    this.closed = true
  }

  open(): void {
    this.onopen?.({} as Event)
  }

  fail(): void {
    this.onerror?.({} as Event)
  }

  dispatch(type: string, data: string): void {
    for (const listener of this.listeners.get(type) ?? []) listener({ data } as MessageEvent)
  }
}

function setup(options: { maxDelay?: number } = {}) {
  const sources: FakeSource[] = []
  const factory = vi.fn((url: string) => {
    const source = new FakeSource(url)
    sources.push(source)
    return source
  })
  const scheduled: { fn: () => void; ms: number; handle: number }[] = []
  const schedule = vi.fn((fn: () => void, ms: number) => {
    const handle = scheduled.length + 1
    scheduled.push({ fn, ms, handle })
    return handle
  })
  const cancel = vi.fn()
  const handlers = { event: vi.fn(), site: vi.fn() }
  const controller = createStream(handlers, { url: '/s', factory, schedule, cancel, baseDelay: 1000, maxDelay: options.maxDelay ?? 30000 })
  // 执行最近一次排队的重连
  const fireNext = () => {
    const next = scheduled[scheduled.length - 1]
    if (next) next.fn()
  }
  return { sources, factory, scheduled, schedule, cancel, handlers, controller, fireNext }
}

describe('backoffDelay', () => {
  it('doubles from base and caps at max', () => {
    expect(backoffDelay(0, 1000, 30000)).toBe(1000)
    expect(backoffDelay(1, 1000, 30000)).toBe(2000)
    expect(backoffDelay(2, 1000, 30000)).toBe(4000)
    expect(backoffDelay(4, 1000, 30000)).toBe(16000)
    expect(backoffDelay(5, 1000, 30000)).toBe(30000)
    expect(backoffDelay(20, 1000, 30000)).toBe(30000)
  })
})

describe('createStream', () => {
  it('connects exactly once on creation', () => {
    const { factory, controller } = setup()
    expect(factory).toHaveBeenCalledTimes(1)
    expect(factory).toHaveBeenCalledWith('/s')
    expect(controller.connected.value).toBe(false)
  })

  it('onopen sets connected and resets attempts', () => {
    const { sources, controller, fireNext } = setup()
    sources[0]!.fail()
    fireNext()
    sources[1]!.fail()
    expect(controller.attempts.value).toBe(2)
    fireNext()
    sources[2]!.open()
    expect(controller.connected.value).toBe(true)
    expect(controller.attempts.value).toBe(0)
  })

  it('onerror closes the source and schedules reconnects with exponential backoff', () => {
    const { sources, scheduled, factory, controller, fireNext } = setup({ maxDelay: 8000 })
    sources[0]!.open()
    expect(controller.connected.value).toBe(true)
    sources[0]!.fail()
    expect(sources[0]!.closed).toBe(true)
    expect(controller.connected.value).toBe(false)
    expect(factory).toHaveBeenCalledTimes(1)
    for (let i = 0; i < 5; i += 1) {
      fireNext()
      sources[sources.length - 1]!.fail()
    }
    expect(scheduled.map((item) => item.ms)).toEqual([1000, 2000, 4000, 8000, 8000, 8000])
    expect(factory).toHaveBeenCalledTimes(6)
    expect(sources.slice(0, 6).every((source) => source.closed)).toBe(true)
  })

  it('dispatches parsed JSON to handlers by event name', () => {
    const { sources, handlers } = setup()
    sources[0]!.dispatch('event', '{"id":1,"type":"restock"}')
    sources[0]!.dispatch('site', '{"id":"s1"}')
    expect(handlers.event).toHaveBeenCalledWith({ id: 1, type: 'restock' })
    expect(handlers.site).toHaveBeenCalledWith({ id: 's1' })
    // 未注册处理器的事件名不监听
    expect(sources[0]!.listeners.has('order')).toBe(false)
  })

  it('ignores invalid JSON', () => {
    const { sources, handlers } = setup()
    expect(() => sources[0]!.dispatch('event', '{not json')).not.toThrow()
    expect(handlers.event).not.toHaveBeenCalled()
  })

  it('close() stops reconnects and closes the source', () => {
    const { sources, scheduled, cancel, factory, controller, fireNext } = setup()
    sources[0]!.fail()
    expect(scheduled).toHaveLength(1)
    controller.close()
    expect(cancel).toHaveBeenCalledWith(scheduled[0]!.handle)
    // 即使定时器仍触发，也不再重连
    fireNext()
    expect(factory).toHaveBeenCalledTimes(1)
    expect(controller.connected.value).toBe(false)
  })

  it('close() closes an open source and no reconnect follows its error', () => {
    const { sources, scheduled, controller } = setup()
    sources[0]!.open()
    controller.close()
    expect(sources[0]!.closed).toBe(true)
    expect(controller.connected.value).toBe(false)
    sources[0]!.fail()
    expect(scheduled).toHaveLength(0)
  })
})
