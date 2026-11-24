// 出口：直连是内置出口（ID 为 direct），与登记的代理一样参与优先级排序与回退
import type { SelectOption } from 'naive-ui'
import type { EgressProxy } from '@/api/types'

export const DIRECT_EGRESS_ID = 'direct'
export const DIRECT_EGRESS_LABEL = '直连'

// 优先级选择框的选项：直连 + 已登记代理（停用的不可选）
export function egressOptions(proxies: readonly EgressProxy[]): SelectOption[] {
  return [
    { label: '直连（本服务器出口）', value: DIRECT_EGRESS_ID },
    ...proxies
      .filter((proxy) => proxy.id)
      .map((proxy) => ({ label: proxy.name || proxy.id, value: proxy.id, disabled: !proxy.enabled })),
  ]
}

export function egressLabel(id: string, proxies: readonly EgressProxy[]): string {
  if (id === DIRECT_EGRESS_ID) return DIRECT_EGRESS_LABEL
  return proxies.find((proxy) => proxy.id === id)?.name || id
}

// 新代理的 8 位十六进制 ID，避开已有 ID 与 direct。
// 不用 crypto.randomUUID：它只在安全上下文（HTTPS / localhost）可用，而管理界面可能经内网 HTTP 访问
export function newEgressId(proxies: readonly EgressProxy[], random: () => string = randomHex8): string {
  const taken = new Set([DIRECT_EGRESS_ID, ...proxies.map((proxy) => proxy.id)])
  let id = random()
  while (taken.has(id)) id = random()
  return id
}

function randomHex8(): string {
  return Array.from(crypto.getRandomValues(new Uint8Array(4)), (byte) => byte.toString(16).padStart(2, '0')).join('')
}
