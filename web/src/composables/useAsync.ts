// 统一的加载 / 错误状态封装，出错时用 naive-ui message 提示
import { ref, type Ref } from 'vue'
import { useMessage } from 'naive-ui'

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
      const text = err instanceof Error ? err.message : String(err)
      error.value = text
      if (!options.silent) message.error(text)
      return null
    } finally {
      loading.value = false
    }
  }
  return { data, loading, error, run }
}

// 执行一次带成功提示的操作，返回是否成功
export function useAction() {
  const message = useMessage()
  return async function act<T>(fn: () => Promise<T>, success?: string): Promise<T | null> {
    try {
      const result = await fn()
      if (success) message.success(success)
      return result
    } catch (err) {
      message.error(err instanceof Error ? err.message : String(err))
      return null
    }
  }
}
