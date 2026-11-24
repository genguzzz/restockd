// 主题：浅色 / 深色 / 跟随系统，记忆在 localStorage
import { computed, ref } from 'vue'
import { darkTheme, useOsTheme } from 'naive-ui'

export type ThemeMode = 'light' | 'dark' | 'auto'

const STORAGE_KEY = 'restockd.theme'

function readMode(): ThemeMode {
  try {
    const value = localStorage.getItem(STORAGE_KEY)
    if (value === 'light' || value === 'dark' || value === 'auto') return value
  } catch {
    // 隐私模式下忽略
  }
  return 'auto'
}

const mode = ref<ThemeMode>(readMode())

export function useTheme() {
  const os = useOsTheme()
  const isDark = computed(() => (mode.value === 'auto' ? os.value === 'dark' : mode.value === 'dark'))
  const theme = computed(() => (isDark.value ? darkTheme : null))
  const setMode = (value: ThemeMode) => {
    mode.value = value
    try {
      localStorage.setItem(STORAGE_KEY, value)
    } catch {
      // 忽略
    }
  }
  const cycle = () => setMode(mode.value === 'auto' ? 'light' : mode.value === 'light' ? 'dark' : 'auto')
  return { mode, isDark, theme, setMode, cycle }
}
