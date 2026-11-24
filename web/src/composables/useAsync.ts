// 统一的加载 / 错误状态封装，出错时用 naive-ui message 提示
import { ref, type Ref } from 'vue'
import { useMessage } from 'naive-ui'

export function errorText(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

export interface AsyncState<T, A extends unknown[]> {
  data: Ref<T | null>
  loading: Ref<boolean>
  error: Ref<string | null>
  run: (...args: A) => Promise<T | null>
}

export function useAsync<T, A extends unknown[] = []>(fn: (...args: A) => Promise<T>, options: { silent?: boolean } = {}): AsyncState<T, A> {
  const message = useMessage()
  const data = ref<T | null>(null) as Ref<T | null>
  const loading = ref(false)
  const error = ref<string | null>(null)
  const run = async (...args: A): Promise<T | null> => {
    loading.value = true
    error.value = null
    try {
      const result = await fn(...args)
      data.value = result
      return result
    } catch (err) {
      const text = errorText(err)
      error.value = text
      if (!options.silent) message.error(text)
      return null
    } finally {
      loading.value = false
    }
  }
  return { data, loading, error, run }
}

// 执行一次操作：成功可选提示，失败提示错误并返回 null
export function useAction() {
  const message = useMessage()
  return async function act<T>(fn: () => Promise<T>, success?: string): Promise<T | null> {
    try {
      const result = await fn()
      if (success) message.success(success)
      return result
    } catch (err) {
      message.error(errorText(err))
      return null
    }
  }
}

// 按 key 记录进行中的操作（如 `${id}:poll`），供按钮 loading 使用
export function useBusy() {
  const keys = ref<Record<string, true>>({})
  const isBusy = (key: string): boolean => keys.value[key] === true
  async function track<T>(key: string, fn: () => Promise<T>): Promise<T> {
    keys.value = { ...keys.value, [key]: true }
    try {
      return await fn()
    } finally {
      const { [key]: _done, ...rest } = keys.value
      keys.value = rest
    }
  }
  return { isBusy, track }
}
