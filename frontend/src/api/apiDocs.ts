/**
 * API 文档目录：内置分类与文档由前端生成，服务端只保存管理员的改动与新增（叠加记录）。
 * 公开读取供未登录访客的文档页使用；管理员逐条增删改，version 用于并发冲突检测。
 */

import { apiClient } from './client'

/** 分类叠加记录；title 为 null 表示沿用内置标题。version=0 为仅保存排序的占位行。 */
export interface ApiDocCategoryOverlay {
  id: string
  title: string | null
  sort: number | null
  hidden: boolean
  custom: boolean
  version: number
  updated_at: string
}

/** 文档叠加记录；可空字段为 null 表示沿用内置值。目录不含正文，has_markdown 表示存在改写正文。 */
export interface ApiDocEntryOverlay {
  id: string
  category_id: string | null
  title: string | null
  markdown?: string | null
  has_markdown: boolean
  sort: number | null
  hidden: boolean
  custom: boolean
  version: number
  updated_at: string
}

export interface ApiDocCatalogOverlay {
  categories: ApiDocCategoryOverlay[]
  documents: ApiDocEntryOverlay[]
}

export interface ApiDocCategoryInput {
  title: string | null
  hidden: boolean
  custom: boolean
  version: number
}

export interface ApiDocEntryInput {
  category_id: string | null
  title: string | null
  markdown: string | null
  hidden: boolean
  custom: boolean
  version: number
}

export interface ApiDocOrderInput {
  categories?: string[]
  documents?: Record<string, string[]>
}

const emptyCatalog = (): ApiDocCatalogOverlay => ({ categories: [], documents: [] })

/** 公开目录（不含已隐藏条目）。 */
export async function getCatalog(): Promise<ApiDocCatalogOverlay> {
  const { data } = await apiClient.get<ApiDocCatalogOverlay>('/settings/api-docs')
  return { ...emptyCatalog(), ...data }
}

/** 公开单篇改写正文；没有改写时服务端返回 404。 */
export async function getDocument(id: string): Promise<ApiDocEntryOverlay> {
  const { data } = await apiClient.get<ApiDocEntryOverlay>(`/settings/api-docs/${encodeURIComponent(id)}`)
  return data
}

/** 管理目录（含已隐藏条目与版本）。 */
export async function getAdminCatalog(): Promise<ApiDocCatalogOverlay> {
  const { data } = await apiClient.get<ApiDocCatalogOverlay>('/admin/settings/api-docs')
  return { ...emptyCatalog(), ...data }
}

export async function getAdminDocument(id: string): Promise<ApiDocEntryOverlay> {
  const { data } = await apiClient.get<ApiDocEntryOverlay>(`/admin/settings/api-docs/documents/${encodeURIComponent(id)}`)
  return data
}

/** 新建（version=0）或修改文档；他人已修改时返回 API_DOC_CONFLICT。 */
export async function saveDocument(id: string, input: ApiDocEntryInput): Promise<ApiDocEntryOverlay> {
  const { data } = await apiClient.put<ApiDocEntryOverlay>(`/admin/settings/api-docs/documents/${encodeURIComponent(id)}`, input)
  return data
}

/** 删除自定义文档，或把内置文档恢复默认（保留排序）。 */
export async function deleteDocument(id: string, version: number): Promise<void> {
  await apiClient.delete(`/admin/settings/api-docs/documents/${encodeURIComponent(id)}`, { params: { version } })
}

export async function saveCategory(id: string, input: ApiDocCategoryInput): Promise<ApiDocCategoryOverlay> {
  const { data } = await apiClient.put<ApiDocCategoryOverlay>(`/admin/settings/api-docs/categories/${encodeURIComponent(id)}`, input)
  return data
}

/** 删除自定义分类（需先清空），或把内置分类恢复默认。 */
export async function deleteCategory(id: string, version: number): Promise<void> {
  await apiClient.delete(`/admin/settings/api-docs/categories/${encodeURIComponent(id)}`, { params: { version } })
}

export async function reorder(input: ApiDocOrderInput): Promise<void> {
  await apiClient.put('/admin/settings/api-docs/order', input)
}

export const apiDocsAPI = {
  getCatalog, getDocument, getAdminCatalog, getAdminDocument,
  saveDocument, deleteDocument, saveCategory, deleteCategory, reorder,
}

export default apiDocsAPI
