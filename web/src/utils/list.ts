// 按 id 增改 / 删除列表项，返回新数组（不修改原数组）
interface HasId {
  id: string | number
}

// 已存在则原位替换，否则追加（prepend 时插到最前）
export function upsertById<T extends HasId>(list: readonly T[], item: T, options: { prepend?: boolean } = {}): T[] {
  const index = list.findIndex((entry) => entry.id === item.id)
  if (index >= 0) return list.map((entry, i) => (i === index ? item : entry))
  return options.prepend ? [item, ...list] : [...list, item]
}

export function removeById<T extends HasId>(list: readonly T[], id: T['id']): T[] {
  return list.filter((entry) => entry.id !== id)
}
