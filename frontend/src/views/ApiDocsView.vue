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
            <nav class="space-y-1" :aria-label="t('apiDocs.quickstart')">
              <router-link v-for="guide in guides" :key="guide.id" :to="`/docs/${guide.id}`" class="doc-nav-link" :class="{ 'doc-nav-active': selectedId === guide.id }" :aria-current="selectedId === guide.id ? 'page' : undefined" @click="mobileCatalog = false">{{ t(guide.label) }}</router-link>
            </nav>
            <nav v-for="category in visibleCategories" :key="category.id" class="space-y-1" :aria-label="category.label">
              <h2 class="mb-2 px-2 text-xs font-semibold text-gray-500 dark:text-dark-400">{{ category.label }} <span class="ml-1 font-normal">{{ category.items.length }}</span></h2>
              <div v-for="group in category.groups" :key="group.id" :class="group.label ? 'pt-2' : ''">
                <h3 v-if="group.label" class="mb-1 flex items-center justify-between gap-2 px-2 text-xs font-semibold text-gray-800 dark:text-dark-100">
                  {{ group.label }}<span class="font-normal text-gray-400 dark:text-dark-400">{{ group.items.length }}</span>
                </h3>
                <div :class="group.label ? 'ml-2 space-y-1 border-l border-gray-200 pl-2 dark:border-dark-600' : 'space-y-1'">
                  <router-link v-for="endpoint in group.items" :key="endpoint.id" :to="`/docs/${endpoint.id}`" class="doc-nav-link" :class="{ 'doc-nav-active': selectedId === endpoint.id }" :aria-current="selectedId === endpoint.id ? 'page' : undefined" @click="mobileCatalog = false">
                    <span class="mr-1.5 font-mono text-[10px] font-bold" :class="methodColor(endpoint.method)">{{ endpoint.method }}</span>{{ endpoint.title }}
                  </router-link>
                </div>
              </div>
            </nav>
            <p v-if="!filteredEndpoints.length" class="px-2 text-sm text-gray-500">{{ t('apiDocs.noResults') }}</p>
          </div>
          <div class="flex items-center justify-between border-t border-gray-100 pt-3 text-xs text-gray-500 dark:border-dark-700">
            <span>{{ filteredEndpoints.length }} {{ t('apiDocs.endpoints') }}</span>
            <button v-if="search || platform" type="button" class="text-primary-600 dark:text-primary-300" @click="search = ''; platform = null">{{ t('apiDocs.reset') }}</button>
          </div>
        </aside>

        <!-- 正文统一按 Markdown 渲染：管理员在系统设置中改写的版本优先，否则使用内置文档 -->
        <article ref="article" class="card min-w-0 scroll-mt-20 p-4 sm:p-6 lg:p-8">
          <template v-if="markdown !== null">
            <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
              <div class="flex min-w-0 flex-wrap items-center gap-2 text-xs text-gray-500 dark:text-dark-400">
                <template v-if="selectedEndpoint">
                  <span>{{ selectedCategory }}</span><Icon name="chevronRight" size="xs" />
                  <template v-if="selectedEndpoint.videoProtocol"><span>{{ videoProtocolLabel(selectedEndpoint.videoProtocol) }}</span><Icon name="chevronRight" size="xs" /></template>
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
import { apiDocCategories, apiDocEndpoints, apiDocPlatforms, apiDocVideoProtocols, filterApiDocEndpoints, platformLabel, videoProtocolLabel } from '@/content/api-docs'
import { apiDocBaseUrl } from '@/content/api-docs/format'
import { apiDocOutline, effectiveApiDocMarkdown, overviewMarkdown } from '@/content/api-docs/markdown'
import { apiDocsAPI } from '@/api/apiDocs'

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
// 管理员改写的文档；读取失败时保持空集合，页面回退到内置文档。
const overrides = ref<Record<string, string>>({})
const isAuthenticated = computed(() => authStore.isAuthenticated)
const siteName = computed(() => appStore.siteName || 'TokenRouter')
const siteLogo = computed(() => sanitizeUrl(appStore.cachedPublicSettings?.site_logo || appStore.siteLogo || '', { allowRelative: true, allowDataUrl: true }))
const baseUrl = computed(() => apiDocBaseUrl(appStore.apiBaseUrl || '', window.location.origin))
const selectedId = computed(() => typeof route.params.documentId === 'string' && route.params.documentId ? route.params.documentId : 'quickstart')
const selectedEndpoint = computed(() => apiDocEndpoints.find(endpoint => endpoint.id === selectedId.value))
const selectedCategory = computed(() => apiDocCategories.find(category => category.id === selectedEndpoint.value?.category)?.label)
// 总览未改写时按目录筛选结果生成表格，与左侧搜索保持一致。
const markdown = computed(() => selectedId.value === 'overview' && !overrides.value.overview?.trim()
  ? overviewMarkdown(filteredEndpoints.value, t('apiDocs.noResults'))
  : effectiveApiDocMarkdown(selectedId.value, baseUrl.value, overrides.value))
const outline = computed(() => markdown.value ? apiDocOutline(markdown.value) : [])
const platformOptions = computed(() => [{ value: null, label: t('apiDocs.allPlatforms') }, ...apiDocPlatforms])
const filteredEndpoints = computed(() => filterApiDocEndpoints(search.value, platform.value))
// 先筛选再生成视频子目录，隐藏没有匹配端点的平台标题。
const visibleCategories = computed(() => apiDocCategories.map(category => {
  const items = filteredEndpoints.value.filter(endpoint => endpoint.category === category.id)
  const groups = category.id === 'video'
    ? apiDocVideoProtocols.map(protocol => ({ ...protocol, items: items.filter(endpoint => endpoint.videoProtocol === protocol.id) })).filter(group => group.items.length)
    : [{ id: category.id, label: '', items }]
  return { ...category, items, groups }
}).filter(category => category.items.length))
const guides = [
  { id: 'quickstart', label: 'apiDocs.quickstart' }, { id: 'auth', label: 'apiDocs.auth' },
  { id: 'overview', label: 'apiDocs.overview' }, { id: 'errors', label: 'apiDocs.errors' },
]

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
// 地址是文档唯一定位依据，浏览器前进后退和直接分享均能恢复相同正文。
watch(selectedId, async () => {
  mobileCatalog.value = false
  await nextTick()
  article.value?.scrollIntoView?.({ block: 'start' })
})
onMounted(() => {
  initTheme()
  if (!appStore.publicSettingsLoaded) void appStore.fetchPublicSettings()
  apiDocsAPI.getOverrides().then(value => { overrides.value = value }).catch(() => { overrides.value = {} })
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
