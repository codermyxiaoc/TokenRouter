import type { ApiDocCatalogOverlay, ApiDocCategoryOverlay, ApiDocEntryOverlay } from '@/api/apiDocs'
import { apiDocCategories, apiDocEndpoints, apiDocVideoProtocols, filterApiDocEndpoints, platformLabel } from './index'
import { API_DOC_GUIDES } from './markdown'
import type { ApiDocEndpoint } from './types'

// 目录 = 内置分类与文档 + 服务端叠加记录：叠加字段为 null 时沿用内置值，未改动的内置文档随版本升级自动更新。
// 目录最多两级：一级分类（如“视频与异步任务”）下可以有二级分类（如“OpenAI Videos”），文档挂在任意一级。
// @project-doc docs/interfaces/api_reference.md#api_docs_markdown

/** 指南固定为内置分类，与接口分类同等参与改名、隐藏与排序。 */
export const API_DOC_GUIDE_CATEGORY = { id: 'guides', label: '指南' }

/** 内置视频协议二级分类的 ID：挂在“视频与异步任务”下。 */
export function builtinVideoSubCategoryId(protocol: string): string {
  return `video-${protocol}`
}

export interface ApiDocCatalogCategory {
  id: string
  title: string
  /** 内置标题；自定义分类为空。 */
  defaultTitle: string
  /** 上级分类 ID；空字符串表示一级分类。 */
  parentId: string
  /** 内置上级；自定义分类与 parentId 相同。 */
  defaultParentId: string
  builtin: boolean
  /** 自身是否被隐藏（删除内置条目即隐藏）。 */
  hidden: boolean
  /** 自身或上级被隐藏时为 true，公开页据此过滤。 */
  effectivelyHidden: boolean
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

/** 目录树节点：一级分类带直属文档与二级分类，二级分类只有直属文档。 */
export interface ApiDocCategoryNode {
  category: ApiDocCatalogCategory
  documents: ApiDocCatalogDocument[]
  children: ApiDocCategoryNode[]
}

interface BuiltinCategory {
  id: string
  label: string
  parentId: string
}

interface BuiltinDocument {
  id: string
  title: string
  categoryId: string
  endpoint?: ApiDocEndpoint
}

const builtinCategories: BuiltinCategory[] = [
  { ...API_DOC_GUIDE_CATEGORY, parentId: '' },
  ...apiDocCategories.map(category => ({ id: category.id as string, label: category.label as string, parentId: '' })),
  // 视频协议原先只是前端自动分组，这里作为真正的二级分类参与改名、隐藏与排序。
  ...apiDocVideoProtocols.map(protocol => ({ id: builtinVideoSubCategoryId(protocol.id), label: protocol.label as string, parentId: 'video' })),
]

const builtinDocuments: BuiltinDocument[] = [
  ...API_DOC_GUIDES.map(guide => ({ id: guide.id, title: guide.title, categoryId: API_DOC_GUIDE_CATEGORY.id })),
  ...apiDocEndpoints.map(endpoint => ({
    id: endpoint.id, title: endpoint.title, endpoint,
    categoryId: endpoint.category === 'video' && endpoint.videoProtocol ? builtinVideoSubCategoryId(endpoint.videoProtocol) : endpoint.category,
  })),
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
 * 合并内置目录与叠加记录。includeHidden 为 false 时去掉隐藏条目、隐藏分类及其子分类下的文档（公开页）。
 * 指向已不存在分类的文档回退到其内置分类，自定义文档回退到指南；上级失效的二级分类回退为一级，避免条目“消失”。
 */
export function buildApiDocCatalog(overlay: ApiDocCatalogOverlay, includeHidden = false): ApiDocCatalog {
  const categoryOverlays = new Map(overlay.categories.map(item => [item.id, item]))
  const documentOverlays = new Map(overlay.documents.map(item => [item.id, item]))

  const raw: Omit<ApiDocCatalogCategory, 'effectivelyHidden'>[] = builtinCategories.map((category, index) => {
    const item = categoryOverlays.get(category.id)
    return {
      id: category.id, title: item?.title || category.label, defaultTitle: category.label,
      parentId: item?.parent_id ?? category.parentId, defaultParentId: category.parentId, builtin: true,
      hidden: !!item?.hidden, sort: item?.sort ?? (index + 1) * 10, overlay: item,
    }
  })
  overlay.categories.filter(item => item.custom && !builtinCategories.some(category => category.id === item.id)).forEach((item, index) => {
    raw.push({ id: item.id, title: item.title || item.id, defaultTitle: '', parentId: item.parent_id ?? '', defaultParentId: item.parent_id ?? '',
      builtin: false, hidden: item.hidden, sort: item.sort ?? UNSORTED_BASE + index, overlay: item })
  })
  // 上级必须存在且本身是一级分类，否则回退为一级，保证目录最多两级。
  const topLevel = new Set(raw.filter(category => !category.parentId).map(category => category.id))
  for (const category of raw) {
    if (category.parentId && (!topLevel.has(category.parentId) || category.parentId === category.id)) category.parentId = ''
  }
  const hiddenSelf = new Map(raw.map(category => [category.id, category.hidden]))
  const categories: ApiDocCatalogCategory[] = raw.map(category => ({
    ...category, effectivelyHidden: category.hidden || (!!category.parentId && !!hiddenSelf.get(category.parentId)),
  }))
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
  const hiddenCategories = new Set(categories.filter(category => category.effectivelyHidden).map(category => category.id))
  return {
    categories: categories.filter(category => !category.effectivelyHidden),
    documents: documents.filter(doc => !doc.hidden && !hiddenCategories.has(doc.categoryId)),
  }
}

/** 已按目录顺序排列的分类直属文档（不含二级分类下的文档）。 */
export function apiDocCategoryDocuments(catalog: ApiDocCatalog, categoryId: string): ApiDocCatalogDocument[] {
  return catalog.documents.filter(doc => doc.categoryId === categoryId)
}

/** 已按目录顺序排列的一级分类。 */
export function apiDocTopCategories(catalog: ApiDocCatalog): ApiDocCatalogCategory[] {
  return catalog.categories.filter(category => !category.parentId)
}

/** 已按目录顺序排列的二级分类。 */
export function apiDocChildCategories(catalog: ApiDocCatalog, parentId: string): ApiDocCatalogCategory[] {
  return catalog.categories.filter(category => category.parentId === parentId)
}

/**
 * 生成目录树；include 用于搜索/平台筛选，只保留命中的文档。
 * keepEmpty 为 false 时去掉没有命中文档的二级分类和一级分类（公开页）；管理端保留空分类以便编辑。
 */
export function apiDocCategoryTree(catalog: ApiDocCatalog, include: (doc: ApiDocCatalogDocument) => boolean = () => true, keepEmpty = false): ApiDocCategoryNode[] {
  const docsOf = (id: string) => apiDocCategoryDocuments(catalog, id).filter(include)
  return apiDocTopCategories(catalog).map(category => {
    const children = apiDocChildCategories(catalog, category.id)
      .map(child => ({ category: child, documents: docsOf(child.id), children: [] }))
      .filter(child => keepEmpty || child.documents.length)
    return { category, documents: docsOf(category.id), children }
  }).filter(node => keepEmpty || node.documents.length || node.children.length)
}

/** 一级分类下的文档总数（含二级分类）。 */
export function apiDocNodeCount(node: ApiDocCategoryNode): number {
  return node.documents.length + node.children.reduce((total, child) => total + child.documents.length, 0)
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

/** 总览表使用的接口列表：只含可见的内置接口，标题采用改名后的值。 */
export function catalogOverviewEndpoints(documents: ApiDocCatalogDocument[]): ApiDocEndpoint[] {
  return documents.filter(doc => doc.endpoint).map(doc => ({ ...doc.endpoint!, title: doc.title }))
}

export { platformLabel }
