// 屏蔽 / 恢复商品（测试品、假价格等）：结果为更新后的站点，写回共享站点列表；屏蔽后提示可撤销
import { h } from 'vue'
import { NButton, useMessage } from 'naive-ui'
import { productsApi } from '@/api'
import { useAction } from './useAsync'
import { upsertSite } from './useSites'

const UNDO_MS = 6000

export function useProductHiding() {
  const message = useMessage()
  const act = useAction()

  const unhide = async (siteId: string, productId: string, success?: string): Promise<boolean> => {
    const site = await act(() => productsApi.unhide(siteId, productId), success)
    if (site) upsertSite(site)
    return site !== null
  }

  // onUndone：撤销成功后（如重新加载列表）
  const hide = async (siteId: string, productId: string, onUndone?: () => void): Promise<boolean> => {
    const site = await act(() => productsApi.hide(siteId, productId))
    if (!site) return false
    upsertSite(site)
    const toast = message.success(
      () =>
        h('span', [
          '已屏蔽 · ',
          h(
            NButton,
            {
              text: true,
              type: 'primary',
              onClick: async () => {
                toast.destroy()
                if (await unhide(siteId, productId, '已撤销')) onUndone?.()
              },
            },
            () => '撤销',
          ),
        ]),
      { duration: UNDO_MS },
    )
    return true
  }

  return { hide, unhide }
}
