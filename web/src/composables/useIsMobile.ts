// 全局窄屏判断（≤768px），多组件共享同一个 matchMedia 监听
import { readonly, ref } from 'vue'

const query = typeof window !== 'undefined' ? window.matchMedia('(max-width: 768px)') : null
const mobile = ref(query?.matches ?? false)
query?.addEventListener('change', (event) => {
  mobile.value = event.matches
})

export function useIsMobile() {
  return readonly(mobile)
}
