import type { ApiDocCatalogOverlay, ApiDocCategoryOverlay, ApiDocEntryOverlay } from '@/api/apiDocs'
import { apiDocCategories, apiDocEndpoints, apiDocVideoProtocols, filterApiDocEndpoints, platformLabel } from './index'
import { API_DOC_GUIDES } from './markdown'
import type { ApiDocEndpoint } from './types'

// 目录 = 内置分类与文档 + 服务端叠加记录：叠加字段为 null 时沿用内置值，未改动的内置文档随版本升级自动更新。
// @project-doc docs/interfaces/api_reference.md#api_docs_markdown

/** 指南固定为内置分类，与接口分类同等参与改名、隐藏与排序。 */
export const API_DOC_GUIDE_CATEGORY = { id: 'guides', label: '指南' }

export interface ApiDocCatalogCategory {
  id: string
  title: string
  /** 内置标题；自定义分类为空。 */
  defaultTitle: string
  builtin: boolean
  hidden: boolean
  sort: number
  /** 叠加记录（无则为 undefined），保存时携带其 version。 */
  overlay?: ApiDocCategoryOverlay
}

export interface ApiDocCatalogDocument {
  id: string
  title: string
  defaultTitle: string
  categoryId: string
  defaultCategoryId: string
  builtin: boolean
  hidden: boolean
  /** 存在改写正文，需要向服务端读取。 */
  hasMarkdown: boolean
  /** 标题、分类、正文或隐藏状态与内置不同。 */
  customized: boolean
  sort: number
  /** 内置接口文档的结构化信息（方法、路径、平台），自定义文档为空。 */
  endpoint?: ApiDocEndpoint
  overlay?: ApiDocEntryOverlay
}

export interface ApiDocCatalog {
  categories: ApiDocCatalogCategory[]
  documents: ApiDocCatalogDocument[]
}

interface BuiltinDocument {
  id: string
  title: string
  categoryId: string
  endpoint?: ApiDocEndpoint
}

const builtinCategories = [API_DOC_GUIDE_CATEGORY, ...apiDocCategories.map(category => ({ id: category.id, label: category.label }))]

const builtinDocuments: BuiltinDocument[] = [
  ...API_DOC_GUIDES.map(guide => ({ id: guide.id, title: guide.title, categoryId: API_DOC_GUIDE_CATEGORY.id })),
  ...apiDocEndpoints.map(endpoint => ({ id: endpoint.id, title: endpoint.title, categoryId: endpoint.category, endpoint })),
]

/** 内置文档的默认标题（编辑器占位提示用）。 */
export function builtinApiDocTitle(id: string): string | undefined {
  return builtinDocuments.find(doc => doc.id === id)?.title
}

/** ID 是否已被内置分类或文档占用，新建自定义条目时须避开。 */
export function isBuiltinApiDocId(id: string): boolean {
  return builtinCategories.some(category => category.id === id) || builtinDocuments.some(doc => doc.id === id)
}

// 未排序条目放在已排序条目之后，保持内置原有顺序；自定义条目追加在末尾。
const UNSORTED_BASE = 100000

/**
 * 合并内置目录与叠加记录。includeHidden 为 false 时去掉隐藏条目及隐藏分类下的文档（公开页）。
 * 指向已不存在分类的文档回退到其内置分类，自定义文档回退到指南，避免文档“消失”。
 */
export function buildApiDocCatalog(overlay: ApiDocCatalogOverlay, includeHidden = false): ApiDocCatalog {
  const categoryOverlays = new Map(overlay.categories.map(item => [item.id, item]))
  const documentOverlays = new Map(overlay.documents.map(item => [item.id, item]))

  const categories: ApiDocCatalogCategory[] = builtinCategories.map((category, index) => {
    const item = categoryOverlays.get(category.id)
    return {
      id: category.id, title: item?.title || category.label, defaultTitle: category.label, builtin: true,
      hidden: !!item?.hidden, sort: item?.sort ?? (index + 1) * 10, overlay: item,
    }
  })
  overlay.categories.filter(item => item.custom && !builtinCategories.some(category => category.id === item.id)).forEach((item, index) => {
    categories.push({ id: item.id, title: item.title || item.id, defaultTitle: '', builtin: false, hidden: item.hidden,
      sort: item.sort ?? UNSORTED_BASE + index, overlay: item })
  })
  categories.sort((a, b) => a.sort - b.sort)
  const categoryIds = new Set(categories.map(category => category.id))

  const documents: ApiDocCatalogDocument[] = []
  const indexInCategory = new Map<string, number>()
  for (const doc of builtinDocuments) {
    const item = documentOverlays.get(doc.id)
    const position = (indexInCategory.get(doc.categoryId) ?? 0) + 1
    indexInCategory.set(doc.categoryId, position)
    const categoryId = item?.category_id && categoryIds.has(item.category_id) ? item.category_id : doc.categoryId
    documents.push({
      id: doc.id, title: item?.title || doc.title, defaultTitle: doc.title, categoryId, defaultCategoryId: doc.categoryId,
      builtin: true, hidden: !!item?.hidden, hasMarkdown: !!item?.has_markdown,
      customized: !!item && item.version > 0 && (!!item.title || !!item.category_id || item.has_markdown || item.hidden),
      sort: item?.sort ?? position * 10, endpoint: doc.endpoint, overlay: item,
    })
  }
  overlay.documents.filter(item => item.custom && !builtinDocuments.some(doc => doc.id === item.id)).forEach((item, index) => {
    const categoryId = item.category_id && categoryIds.has(item.category_id) ? item.category_id : API_DOC_GUIDE_CATEGORY.id
    documents.push({
      id: item.id, title: item.title || item.id, defaultTitle: '', categoryId, defaultCategoryId: categoryId, builtin: false,
      hidden: item.hidden, hasMarkdown: item.has_markdown, customized: true, sort: item.sort ?? UNSORTED_BASE + index, overlay: item,
    })
  })
  documents.sort((a, b) => a.sort - b.sort)

  if (includeHidden) return { categories, documents }
  const hiddenCategories = new Set(categories.filter(category => category.hidden).map(category => category.id))
  return {
    categories: categories.filter(category => !category.hidden),
    documents: documents.filter(doc => !doc.hidden && !hiddenCategories.has(doc.categoryId)),
  }
}

/** 已按目录顺序排列的分类内文档。 */
export function apiDocCategoryDocuments(catalog: ApiDocCatalog, categoryId: string): ApiDocCatalogDocument[] {
  return catalog.documents.filter(doc => doc.categoryId === categoryId)
}

/**
 * 按搜索词与平台筛选：内置接口沿用原有检索范围（含改名后的标题）；
 * 指南与自定义文档没有平台信息，只按标题检索，平台筛选不影响它们。
 */
export function filterApiDocCatalog(documents: ApiDocCatalogDocument[], search: string, platform: string | null): ApiDocCatalogDocument[] {
  const terms = search.trim().toLocaleLowerCase().split(/\s+/).filter(Boolean)
  const matchedEndpoints = new Set(filterApiDocEndpoints('', platform).map(endpoint => endpoint.id))
  return documents.filter(doc => {
    if (doc.endpoint) {
      if (!matchedEndpoints.has(doc.id)) return false
      if (!terms.length) return true
      const byBuiltin = filterApiDocEndpoints(search, platform).some(endpoint => endpoint.id === doc.id)
      return byBuiltin || terms.every(term => doc.title.toLocaleLowerCase().includes(term))
    }
    return terms.every(term => doc.title.toLocaleLowerCase().includes(term))
  })
}

/**
 * 分类内按视频协议子分组：协议组保持内置协议顺序，组内沿用管理员排序；
 * 没有协议的文档（含自定义文档）组成无标题组，排在协议组之前。
 */
export function groupApiDocDocuments(documents: ApiDocCatalogDocument[]): { id: string; label: string; items: ApiDocCatalogDocument[] }[] {
  const plain = documents.filter(doc => !doc.endpoint?.videoProtocol)
  const groups = apiDocVideoProtocols
    .map(protocol => ({ id: protocol.id as string, label: protocol.label as string, items: documents.filter(doc => doc.endpoint?.videoProtocol === protocol.id) }))
    .filter(group => group.items.length)
  return plain.length ? [{ id: '', label: '', items: plain }, ...groups] : groups
}

/** 总览表使用的接口列表：只含可见的内置接口，标题采用改名后的值。 */
export function catalogOverviewEndpoints(documents: ApiDocCatalogDocument[]): ApiDocEndpoint[] {
  return documents.filter(doc => doc.endpoint).map(doc => ({ ...doc.endpoint!, title: doc.title }))
}

export { platformLabel }
