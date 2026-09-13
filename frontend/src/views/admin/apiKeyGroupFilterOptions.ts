import type { AdminGroup } from '@/types'

export interface ApiKeyGroupFilterOption {
  value: number | null
  label: string
  kind?: 'group'
  disabled?: boolean
}

export interface ApiKeyGroupFilterLabels {
  all: string
  exclusive: string
  public: string
  disabled: string
}

// 分区标题行使用负数哨兵值，避免和真实分组 ID 冲突。
// Select.vue 会用 `${typeof value}:${String(value ?? '')}` 生成 :key，
// 使用不同数字可避免多个 null 标题行产生重复的 "object:" key。
const HEADER_EXCLUSIVE = -1
const HEADER_PUBLIC = -2
const HEADER_DISABLED = -3

/**
 * 构建“API Key 分组”筛选 Select 的选项。
 *
 * 启用分组按专属 / 公开分区，每个非空分区前放一个禁用的标题行。
 * 禁用分组集中放到最后的“已禁用”分区，便于管理员筛选 Key 仍绑定在禁用分组上的用户。
 * 当前 fork 的订阅套餐不再挂载 group_id，因此这里不引入 upstream 的 subscription_type 分区。
 * 空分区不渲染标题；最前面的“全部”选项（value 为 null）用于清除筛选。
 *
 * 分区标题行使用负数哨兵值而不是 null，
 * 这样 Vue 的 v-for :key 会生成不同字符串，避免重复 key 告警。
 */
export function buildApiKeyGroupFilterOptions(
  groups: AdminGroup[],
  labels: ApiKeyGroupFilterLabels
): ApiKeyGroupFilterOption[] {
  const exclusive: ApiKeyGroupFilterOption[] = []
  const publicGroups: ApiKeyGroupFilterOption[] = []
  const disabledGroups: ApiKeyGroupFilterOption[] = []

  for (const grp of groups) {
    const item: ApiKeyGroupFilterOption = { value: grp.id, label: grp.name }
    if (grp.status !== 'active') {
      disabledGroups.push(item)
    } else if (grp.is_exclusive) {
      exclusive.push(item)
    } else {
      publicGroups.push(item)
    }
  }

  const options: ApiKeyGroupFilterOption[] = [{ value: null, label: labels.all }]

  const sections: Array<[string, number, ApiKeyGroupFilterOption[]]> = [
    [labels.exclusive, HEADER_EXCLUSIVE, exclusive],
    [labels.public, HEADER_PUBLIC, publicGroups],
    [labels.disabled, HEADER_DISABLED, disabledGroups],
  ]
  for (const [label, headerValue, items] of sections) {
    if (items.length === 0) continue
    options.push({ value: headerValue, label, kind: 'group', disabled: true })
    options.push(...items)
  }
  return options
}
