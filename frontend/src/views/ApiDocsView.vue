<template>
  <component :is="isAuthenticated ? AppLayout : 'div'" :class="isAuthenticated ? '' : 'ba-theme-shell relative min-h-screen'">
    <template v-if="!isAuthenticated">
      <div class="ba-theme-backdrop pointer-events-none fixed inset-0"></div>
      <header class="glass relative z-20 border-b border-primary-900/10 dark:border-dark-600/80">
        <nav class="mx-auto flex h-14 max-w-[1600px] items-center justify-between gap-3 px-4 sm:px-6">
          <router-link to="/home" class="flex min-w-0 items-center gap-2.5">
            <img :src="siteLogo || '/logo.svg'" :alt="siteName" class="h-8 w-8 rounded-lg object-contain" />
            <span class="truncate font-semibold text-gray-950 dark:text-white">{{ siteName }}</span>
          </router-link>
          <div class="flex shrink-0 items-center gap-3 text-sm">
            <router-link to="/models" class="hidden text-gray-600 hover:text-primary-600 dark:text-dark-300 sm:block">{{ t('apiDocs.models') }}</router-link>
            <button type="button" class="btn-ghost btn-icon" :aria-label="isDark ? t('home.switchToLight') : t('home.switchToDark')" @click="toggleTheme"><Icon :name="isDark ? 'sun' : 'moon'" size="md" /></button>
            <LocaleSwitcher />
            <router-link to="/login" class="btn btn-primary btn-sm">{{ t('apiDocs.signIn') }}</router-link>
          </div>
        </nav>
      </header>
    </template>

    <div class="relative mx-auto min-w-0 max-w-[1600px]" :class="isAuthenticated ? '' : 'px-4 py-6 sm:px-6'">
      <div v-if="!isAuthenticated" class="page-heading mb-5">
        <h1 class="page-title">{{ t('apiDocs.title') }}</h1>
        <p class="page-description">{{ t('apiDocs.subtitle') }}</p>
      </div>
      <div class="mb-4 flex items-center justify-between gap-3 lg:hidden">
        <button type="button" class="btn btn-secondary" :aria-expanded="mobileCatalog" aria-controls="api-doc-catalog" @click="mobileCatalog = !mobileCatalog"><Icon name="menu" size="sm" />{{ t('apiDocs.catalog') }}</button>
        <router-link v-if="isAuthenticated" :to="authStore.isAdmin ? '/admin/dashboard' : '/dashboard'" class="text-sm text-primary-600 dark:text-primary-300">{{ t('apiDocs.back') }}</router-link>
      </div>

      <div class="api-doc-layout items-start gap-5" :class="{ 'has-outline': outline.length > 1 }">
        <aside id="api-doc-catalog" class="card min-w-0 space-y-4 p-4 lg:sticky lg:top-[76px]" :class="mobileCatalog ? 'block' : 'hidden lg:block'" :aria-label="t('apiDocs.catalog')">
          <SearchInput v-model="search" :placeholder="t('apiDocs.search')" />
          <div>
            <label class="input-label">{{ t('apiDocs.platform') }}</label>
            <Select v-model="platform" :options="platformOptions" searchable />
          </div>
          <div class="max-h-[65dvh] space-y-5 overflow-y-auto overscroll-contain pr-1">
            <nav v-for="node in visibleCategories" :key="node.category.id" class="space-y-1" :aria-label="node.category.title">
              <h2 class="mb-2 px-2 text-xs font-semibold text-gray-500 dark:text-dark-400">{{ node.category.title }} <span v-if="node.category.id !== guideCategoryId" class="ml-1 font-normal">{{ apiDocNodeCount(node) }}</span></h2>
              <div v-if="node.documents.length" class="space-y-1">
                <router-link v-for="doc in node.documents" :key="doc.id" :to="`/docs/${doc.id}`" class="doc-nav-link" :class="{ 'doc-nav-active': selectedId === doc.id }" :aria-current="selectedId === doc.id ? 'page' : undefined" @click="mobileCatalog = false">
                  <span v-if="doc.endpoint" class="mr-1.5 font-mono text-[10px] font-bold" :class="methodColor(doc.endpoint.method)">{{ doc.endpoint.method }}</span>{{ doc.title }}
                </router-link>
              </div>
              <!-- 二级分类：标题可在后台改名、隐藏或删除，组内文档按后台顺序排列 -->
              <div v-for="child in node.children" :key="child.category.id" class="pt-2">
                <h3 class="mb-1 flex items-center justify-between gap-2 px-2 text-xs font-semibold text-gray-800 dark:text-dark-100">
                  {{ child.category.title }}<span class="font-normal text-gray-400 dark:text-dark-400">{{ child.documents.length }}</span>
                </h3>
                <div class="ml-2 space-y-1 border-l border-gray-200 pl-2 dark:border-dark-600">
                  <router-link v-for="doc in child.documents" :key="doc.id" :to="`/docs/${doc.id}`" class="doc-nav-link" :class="{ 'doc-nav-active': selectedId === doc.id }" :aria-current="selectedId === doc.id ? 'page' : undefined" @click="mobileCatalog = false">
                    <span v-if="doc.endpoint" class="mr-1.5 font-mono text-[10px] font-bold" :class="methodColor(doc.endpoint.method)">{{ doc.endpoint.method }}</span>{{ doc.title }}
                  </router-link>
                </div>
              </div>
            </nav>
            <p v-if="!filteredDocuments.length" class="px-2 text-sm text-gray-500">{{ t('apiDocs.noResults') }}</p>
          </div>
          <div class="flex items-center justify-between border-t border-gray-100 pt-3 text-xs text-gray-500 dark:border-dark-700">
            <span>{{ filteredEndpointCount }} {{ t('apiDocs.endpoints') }}</span>
            <button v-if="search || platform" type="button" class="text-primary-600 dark:text-primary-300" @click="search = ''; platform = null">{{ t('apiDocs.reset') }}</button>
          </div>
        </aside>

        <!-- 正文统一按 Markdown 渲染：管理员在系统设置中改写的版本优先，否则使用内置文档；已隐藏的文档按不存在处理 -->
        <article ref="article" class="card min-w-0 scroll-mt-20 p-4 sm:p-6 lg:p-8">
          <template v-if="markdown !== null">
            <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
              <div class="flex min-w-0 flex-wrap items-center gap-2 text-xs text-gray-500 dark:text-dark-400">
                <template v-if="selectedEndpoint">
                  <template v-if="selectedParentCategory"><span>{{ selectedParentCategory }}</span><Icon name="chevronRight" size="xs" /></template>
                  <span>{{ selectedCategory }}</span><Icon name="chevronRight" size="xs" />
                  <span v-for="id in selectedEndpoint.platforms" :key="id" class="rounded-md bg-gray-100 px-2 py-1 dark:bg-dark-700">{{ platformLabel(id) }}</span>
                </template>
              </div>
              <div class="flex gap-2">
                <button type="button" class="btn btn-secondary btn-sm" @click="copyToClipboard(markdown)"><Icon name="clipboard" size="sm" />{{ t('apiDocs.copyMarkdown') }}</button>
                <button type="button" class="btn-ghost btn-icon" :aria-label="t('apiDocs.copyLink')" @click="copyLink"><Icon name="link" size="sm" /></button>
              </div>
            </div>
            <ApiDocMarkdown :markdown="markdown" />
          </template>
          <template v-else><h2 class="doc-title">{{ t('apiDocs.notFound') }}</h2><p class="doc-prose">{{ t('apiDocs.notFoundHint') }}</p><router-link to="/docs" class="btn btn-primary mt-5">{{ t('apiDocs.quickstart') }}</router-link></template>
        </article>

        <aside v-if="outline.length > 1" class="hidden min-w-0 space-y-3 2xl:sticky 2xl:top-[88px] 2xl:block" :aria-label="t('apiDocs.outline')">
          <h2 class="text-xs font-semibold text-gray-900 dark:text-white">{{ t('apiDocs.outline') }}</h2>
          <button v-for="(heading, index) in outline" :key="`${index}-${heading}`" type="button" class="block border-l-2 border-gray-200 py-1 pl-3 text-left text-xs text-gray-500 hover:border-primary-400 hover:text-primary-600 dark:border-dark-600 dark:text-dark-300" @click="scrollToHeading(index)">{{ heading }}</button>
        </aside>
      </div>
    </div>
  </component>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useAppStore, useAuthStore } from '@/stores'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import LocaleSwitcher from '@/components/common/LocaleSwitcher.vue'
import Select from '@/components/common/Select.vue'
import SearchInput from '@/components/common/SearchInput.vue'
import ApiDocMarkdown from '@/components/api-docs/ApiDocMarkdown.vue'
import { useClipboard } from '@/composables/useClipboard'
import { initTheme, useTheme } from '@/composables/useTheme'
import { sanitizeUrl } from '@/utils/url'
import { apiDocPlatforms, platformLabel } from '@/content/api-docs'
import { apiDocBaseUrl } from '@/content/api-docs/format'
import { apiDocOutline, overviewMarkdown, resolveApiDocMarkdown } from '@/content/api-docs/markdown'
import { API_DOC_GUIDE_CATEGORY, apiDocCategoryTree, apiDocNodeCount, buildApiDocCatalog, catalogOverviewEndpoints, filterApiDocCatalog } from '@/content/api-docs/catalog'
import { apiDocsAPI, type ApiDocCatalogOverlay } from '@/api/apiDocs'

const { t } = useI18n()
const route = useRoute()
const appStore = useAppStore()
const authStore = useAuthStore()
const { isDark, toggleTheme } = useTheme()
const { copyToClipboard } = useClipboard()
const search = ref('')
const platform = ref<string | null>(null)
const mobileCatalog = ref(false)
const article = ref<HTMLElement | null>(null)
// 管理员的目录叠加；读取失败时保持空集合，页面回退到内置目录。
const overlay = ref<ApiDocCatalogOverlay>({ categories: [], documents: [] })
// 当前文档的改写正文：undefined 表示仍在读取，null 表示没有改写。
const customMarkdown = ref<string | null | undefined>(undefined)
const catalog = computed(() => buildApiDocCatalog(overlay.value))
const guideCategoryId = API_DOC_GUIDE_CATEGORY.id
const isAuthenticated = computed(() => authStore.isAuthenticated)
const siteName = computed(() => appStore.siteName || 'TokenRouter')
const siteLogo = computed(() => sanitizeUrl(appStore.cachedPublicSettings?.site_logo || appStore.siteLogo || '', { allowRelative: true, allowDataUrl: true }))
const baseUrl = computed(() => apiDocBaseUrl(appStore.apiBaseUrl || '', window.location.origin))
const selectedId = computed(() => typeof route.params.documentId === 'string' && route.params.documentId ? route.params.documentId : 'quickstart')
const selectedDocument = computed(() => catalog.value.documents.find(doc => doc.id === selectedId.value))
const selectedEndpoint = computed(() => selectedDocument.value?.endpoint)
const selectedCategoryRecord = computed(() => catalog.value.categories.find(category => category.id === selectedDocument.value?.categoryId))
const selectedCategory = computed(() => selectedCategoryRecord.value?.title)
// 二级分类的文档在面包屑中同时显示所属一级分类。
const selectedParentCategory = computed(() => {
  const parentId = selectedCategoryRecord.value?.parentId
  return parentId ? catalog.value.categories.find(category => category.id === parentId)?.title : undefined
})
// 总览未改写时按目录筛选结果生成表格，与左侧搜索保持一致；隐藏或不存在的文档显示未找到。
const markdown = computed(() => {
  const doc = selectedDocument.value
  if (!doc) return null
  const custom = doc.hasMarkdown ? customMarkdown.value : null
  if (doc.id === 'overview' && !custom?.trim()) return overviewMarkdown(catalogOverviewEndpoints(filteredDocuments.value), t('apiDocs.noResults'))
  // 自定义文档没有内置正文，正文读取完成前保持空白而不是显示“未找到”。
  if (!doc.builtin && custom === undefined) return ''
  return resolveApiDocMarkdown(doc.id, baseUrl.value, custom)
})
const outline = computed(() => markdown.value ? apiDocOutline(markdown.value) : [])
const platformOptions = computed(() => [{ value: null, label: t('apiDocs.allPlatforms') }, ...apiDocPlatforms])
const filteredDocuments = computed(() => filterApiDocCatalog(catalog.value.documents, search.value, platform.value))
const filteredEndpointCount = computed(() => filteredDocuments.value.filter(doc => doc.endpoint).length)
// 先筛选再生成两级目录，隐藏没有匹配文档的一级与二级分类；分类与文档按管理员设置的顺序排列。
const visibleCategories = computed(() => {
  const ids = new Set(filteredDocuments.value.map(doc => doc.id))
  return apiDocCategoryTree(catalog.value, doc => ids.has(doc.id))
})

function methodColor(method: string): string {
  if (method === 'GET') return 'text-emerald-600 dark:text-emerald-400'
  if (method === 'DELETE') return 'text-red-600 dark:text-red-400'
  if (method === 'WS') return 'text-violet-600 dark:text-violet-400'
  return 'text-blue-600 dark:text-blue-400'
}
function scrollToHeading(index: number) {
  document.getElementById(`doc-heading-${index}`)?.scrollIntoView({ behavior: 'smooth', block: 'start' })
}
function copyLink() {
  void copyToClipboard(new URL(`/docs/${selectedId.value}`, window.location.origin).href)
}
// 只为存在改写正文的文档读取正文，忽略切换后才返回的旧请求。
let markdownRequest = 0
async function loadCustomMarkdown() {
  const request = ++markdownRequest
  const doc = selectedDocument.value
  if (!doc?.hasMarkdown) {
    customMarkdown.value = null
    return
  }
  customMarkdown.value = undefined
  try {
    const entry = await apiDocsAPI.getDocument(doc.id)
    if (request === markdownRequest) customMarkdown.value = entry.markdown ?? null
  } catch {
    if (request === markdownRequest) customMarkdown.value = null
  }
}
watch(() => [selectedId.value, selectedDocument.value?.overlay?.version], () => { void loadCustomMarkdown() }, { immediate: true })
// 地址是文档唯一定位依据，浏览器前进后退和直接分享均能恢复相同正文。
watch(selectedId, async () => {
  mobileCatalog.value = false
  await nextTick()
  article.value?.scrollIntoView?.({ block: 'start' })
})
onMounted(() => {
  initTheme()
  if (!appStore.publicSettingsLoaded) void appStore.fetchPublicSettings()
  apiDocsAPI.getCatalog().then(value => { overlay.value = value }).catch(() => { overlay.value = { categories: [], documents: [] } })
})
</script>

<style scoped>
.api-doc-layout { display: grid; grid-template-columns: minmax(0, 1fr); }
@media (min-width: 1024px) { .api-doc-layout { grid-template-columns: 230px minmax(0, 1fr); } }
@media (min-width: 1536px) { .api-doc-layout.has-outline { grid-template-columns: 240px minmax(0, 1fr) 160px; } }
.doc-nav-link { @apply block rounded-lg px-2 py-2 text-xs leading-5 text-gray-600 transition-colors hover:bg-gray-100 dark:text-dark-300 dark:hover:bg-dark-700; }
.doc-nav-active { @apply bg-primary-50 font-medium text-primary-700 dark:bg-primary-900/30 dark:text-primary-300; }
.doc-title { @apply mb-4 text-2xl font-bold tracking-tight text-gray-900 dark:text-white; }
.doc-prose { @apply break-words text-sm leading-7 text-gray-600 dark:text-dark-300; }
</style>
