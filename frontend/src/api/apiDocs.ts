/**
 * API 文档 Markdown 改写：公开读取（未登录访客的文档页同样需要），管理员整体替换。
 */

import { apiClient } from './client'

export async function getOverrides(): Promise<Record<string, string>> {
  const { data } = await apiClient.get<{ overrides: Record<string, string> }>('/settings/api-docs')
  return data?.overrides ?? {}
}

/** 整体替换改写集合；正文为空的文档恢复内置默认内容。 */
export async function updateOverrides(overrides: Record<string, string>): Promise<Record<string, string>> {
  const { data } = await apiClient.put<{ overrides: Record<string, string> }>('/admin/settings/api-docs', { overrides })
  return data?.overrides ?? {}
}

/** 单篇改写：markdown 为空表示恢复默认；base 为编辑起点（未改写为空），他人已修改时返回 API_DOC_CONFLICT。 */
export interface ApiDocOverridePatch {
  markdown: string
  base: string
}

/** 只合并本次修改的文档，不同管理员修改不同文档不会互相覆盖。 */
export async function patchOverrides(documents: Record<string, ApiDocOverridePatch>): Promise<Record<string, string>> {
  const { data } = await apiClient.patch<{ overrides: Record<string, string> }>('/admin/settings/api-docs', { documents })
  return data?.overrides ?? {}
}

export const apiDocsAPI = { getOverrides, updateOverrides, patchOverrides }

export default apiDocsAPI
