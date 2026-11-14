/**
 * 请求数据缓存（数据缓存设置）管理端接口：普通请求与错误请求分开配置，支持统计与手动清除。
 */
import { apiClient } from '../client'

/** 一类请求（普通/错误）的缓存配置。 */
export interface RequestDataCacheScope {
  enabled: boolean
  /** 缓存时长（分钟）。 */
  ttl_minutes: number
  /** 为 true 时缓存全部分组，否则只缓存 group_ids 中的分组。 */
  all_groups: boolean
  group_ids: number[]
  /** 未绑定分组的请求是否缓存，仅在 all_groups 为 false 时生效。 */
  include_ungrouped: boolean
}

export interface RequestDataCacheSettings {
  normal: RequestDataCacheScope
  error: RequestDataCacheScope
}

/** 两类缓存当前的有效条数。 */
export interface RequestDataCacheStats {
  normal: number
  error: number
}

export interface RequestDataCacheView {
  settings: RequestDataCacheSettings
  stats: RequestDataCacheStats
}

export type RequestDataCacheKind = 'normal' | 'error' | 'all'

export async function getRequestDataCache(): Promise<RequestDataCacheView> {
  const { data } = await apiClient.get<RequestDataCacheView>('/admin/ops/request-data-cache')
  return data
}

export async function updateRequestDataCache(settings: RequestDataCacheSettings): Promise<RequestDataCacheView> {
  const { data } = await apiClient.put<RequestDataCacheView>('/admin/ops/request-data-cache', settings)
  return data
}

export async function clearRequestDataCache(kind: RequestDataCacheKind): Promise<RequestDataCacheView & { deleted: number }> {
  const { data } = await apiClient.post<RequestDataCacheView & { deleted: number }>('/admin/ops/request-data-cache/clear', { kind })
  return data
}

export const requestDataCacheAPI = {
  get: getRequestDataCache,
  update: updateRequestDataCache,
  clear: clearRequestDataCache
}

export default requestDataCacheAPI
