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

      <div class="api-doc-layout items-start gap-5" :class="{ 'has-outline': selectedEndpoint }">
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

        <article ref="article" class="card min-w-0 scroll-mt-20 p-4 sm:p-6 lg:p-8">
          <template v-if="selectedEndpoint">
            <div class="mb-3 flex flex-wrap items-center gap-2 text-xs text-gray-500 dark:text-dark-400">
              <span>{{ selectedCategory }}</span><Icon name="chevronRight" size="xs" />
              <template v-if="selectedEndpoint.videoProtocol"><span>{{ videoProtocolLabel(selectedEndpoint.videoProtocol) }}</span><Icon name="chevronRight" size="xs" /></template>
              <span v-for="id in selectedEndpoint.platforms" :key="id" class="rounded-md bg-gray-100 px-2 py-1 dark:bg-dark-700">{{ platformLabel(id) }}</span>
            </div>
            <div class="mb-4 flex flex-wrap items-start justify-between gap-3">
              <h2 class="text-2xl font-bold tracking-tight text-gray-900 dark:text-white">{{ selectedEndpoint.title }}</h2>
              <div class="flex gap-2">
                <button type="button" class="btn btn-secondary btn-sm" @click="copyToClipboard(apiDocMarkdown(selectedEndpoint, baseUrl))"><Icon name="clipboard" size="sm" />{{ t('apiDocs.copyMarkdown') }}</button>
                <button type="button" class="btn-ghost btn-icon" :aria-label="t('apiDocs.copyLink')" @click="copyLink"><Icon name="link" size="sm" /></button>
              </div>
            </div>
            <p class="doc-prose">{{ selectedEndpoint.summary }}</p>
            <div class="my-5 flex items-start gap-3 rounded-xl border border-primary-200 bg-primary-50 p-4 dark:border-primary-800 dark:bg-primary-900/20">
              <span class="shrink-0 rounded-md bg-white px-2 py-1 font-mono text-xs font-bold dark:bg-dark-800" :class="methodColor(selectedEndpoint.method)">{{ selectedEndpoint.method }}</span>
              <code class="min-w-0 break-all text-sm leading-6 text-gray-900 dark:text-white">{{ selectedEndpoint.path }}</code>
            </div>
            <p class="mb-6 text-xs leading-6 text-gray-500 dark:text-dark-400">{{ t('apiDocs.platformNotice') }}</p>

            <section id="doc-authorization" class="doc-section">
              <h3 class="doc-section-title">{{ t('apiDocs.authorization') }}</h3>
              <ApiDocCodeBlock :code="apiDocAuthExample(selectedEndpoint)" language="HTTP headers" />
            </section>
            <section id="doc-parameters" class="doc-section">
              <h3 class="doc-section-title">{{ t('apiDocs.parameters') }}</h3>
              <p v-if="selectedEndpoint.contentType" class="mb-3 text-sm text-gray-500">Content-Type: <code>{{ selectedEndpoint.contentType }}</code></p>
              <div v-if="selectedEndpoint.parameters.length" class="doc-table-wrapper">
                <table class="doc-table">
                  <thead><tr><th>{{ t('apiDocs.field') }}</th><th>{{ t('apiDocs.type') }}</th><th>{{ t('apiDocs.required') }}</th><th>{{ t('apiDocs.description') }}</th></tr></thead>
                  <tbody><tr v-for="parameter in selectedEndpoint.parameters" :key="parameter.name">
                    <td><code class="text-primary-700 dark:text-primary-300">{{ parameter.name }}</code></td><td class="text-xs">{{ parameter.type }}</td>
                    <td class="whitespace-nowrap"><span :class="parameter.required ? 'text-amber-700 dark:text-amber-300' : 'text-gray-400'">{{ t(parameter.required === true ? 'apiDocs.yes' : parameter.required === 'conditional' ? 'apiDocs.conditional' : 'apiDocs.no') }}</span></td><td>{{ parameter.description }}</td>
                  </tr></tbody>
                </table>
              </div>
              <p v-else class="doc-prose">{{ t('apiDocs.noParameters') }}</p>
            </section>
            <section id="doc-request" class="doc-section space-y-3">
              <h3 class="doc-section-title">{{ t('apiDocs.request') }}</h3>
              <ApiDocCodeBlock :code="apiDocEnvironment(baseUrl)" :label="t('apiDocs.environment')" />
              <p v-if="selectedEndpoint.path.includes('{')" class="text-xs leading-6 text-gray-500 dark:text-dark-400">路径中的占位符需替换为实际模型、任务 ID 或资源 ID；查询任务时使用创建响应返回的 ID。</p>
              <ApiDocCodeBlock :code="apiDocCurl(selectedEndpoint)" :language="selectedEndpoint.method === 'WS' ? 'WebSocket' : 'cURL · Bash'" />
              <ApiDocCodeBlock v-if="selectedEndpoint.requestExample && selectedEndpoint.requestLanguage !== 'bash' && selectedEndpoint.requestLanguage !== 'shell' && selectedEndpoint.method !== 'WS'" :code="selectedEndpoint.requestExample" :language="selectedEndpoint.requestLanguage || 'JSON'" />
            </section>
            <section id="doc-response" class="doc-section space-y-3">
              <h3 class="doc-section-title">{{ t('apiDocs.response') }} <span v-if="selectedEndpoint.responseStatus" class="ml-2 text-sm font-normal text-emerald-600">HTTP {{ selectedEndpoint.responseStatus }}</span></h3>
              <p class="text-xs leading-6 text-gray-500 dark:text-dark-400">{{ t('apiDocs.sampleNotice') }}</p>
              <p v-if="selectedEndpoint.responseDescription" class="doc-prose">{{ selectedEndpoint.responseDescription }}</p>
              <ApiDocCodeBlock :code="selectedEndpoint.responseExample" :language="selectedEndpoint.responseLanguage || 'JSON'" />
            </section>
            <section v-if="selectedEndpoint.notes?.length" id="doc-notes" class="doc-section">
              <h3 class="doc-section-title">{{ t('apiDocs.notes') }}</h3>
              <ul class="list-disc space-y-2 pl-5"><li v-for="note in selectedEndpoint.notes" :key="note" class="doc-prose">{{ note }}</li></ul>
            </section>
            <section v-if="selectedEndpoint.aliases?.length" id="doc-aliases" class="doc-section">
              <h3 class="doc-section-title">{{ t('apiDocs.aliases') }}</h3>
              <div class="space-y-2"><code v-for="alias in selectedEndpoint.aliases" :key="alias" class="block break-all rounded-lg bg-gray-50 p-3 text-xs dark:bg-dark-800">{{ alias }}</code></div>
            </section>
            <section id="doc-errors" class="doc-section">
              <h3 class="doc-section-title">{{ t('apiDocs.errors') }}</h3>
              <ul class="space-y-3"><li v-for="error in selectedEndpoint.errors?.length ? selectedEndpoint.errors : commonApiErrors.slice(0, 4)" :key="`${error.status}-${error.code}`" class="doc-prose"><span class="font-mono font-semibold text-gray-900 dark:text-white">{{ error.status }} · {{ error.code }}</span><p>{{ error.description }}</p></li></ul>
              <router-link to="/docs/errors" class="mt-4 inline-flex items-center gap-1 text-sm text-primary-600 dark:text-primary-300">{{ t('apiDocs.errors') }}<Icon name="arrowRight" size="sm" /></router-link>
            </section>
          </template>

          <template v-else-if="selectedId === 'overview'">
            <h2 class="doc-title">{{ t('apiDocs.overview') }}</h2>
            <p class="doc-prose mb-5">{{ t('apiDocs.platformNotice') }} 使用左侧搜索和平台筛选缩小范围；点击接口查看完整调用说明。</p>
            <div class="doc-table-wrapper"><table class="doc-table"><thead><tr><th>{{ t('apiDocs.method') }}</th><th>{{ t('apiDocs.path') }}</th><th>{{ t('apiDocs.description') }}</th></tr></thead><tbody>
              <tr v-for="endpoint in filteredEndpoints" :key="endpoint.id"><td :class="methodColor(endpoint.method)">{{ endpoint.method }}</td><td><router-link :to="`/docs/${endpoint.id}`" class="font-mono text-xs text-primary-600 dark:text-primary-300">{{ endpoint.path }}</router-link></td><td>{{ endpoint.title }}</td></tr>
            </tbody></table></div>
            <p v-if="!filteredEndpoints.length" class="doc-prose mt-4">{{ t('apiDocs.noResults') }}</p>
          </template>

          <template v-else-if="selectedId === 'auth'">
            <h2 class="doc-title">{{ t('apiDocs.auth') }}</h2>
            <p class="doc-prose">使用本站创建的 API Key，而不是登录令牌或上游厂商密钥。常规接口使用 Bearer；Anthropic 和 Gemini 原生入口也支持各自的认证头。</p>
            <div class="mt-5 space-y-4"><ApiDocCodeBlock :code="apiDocAuthExample()" language="OpenAI / Video / 通用" /><ApiDocCodeBlock code="x-api-key: $TOKENROUTER_API_KEY&#10;anthropic-version: 2023-06-01" language="Anthropic Messages" /><ApiDocCodeBlock code="x-goog-api-key: $TOKENROUTER_API_KEY" language="Gemini 原生" /></div>
            <h3 class="doc-section-title mt-7">接入约定</h3>
            <ul class="list-disc space-y-3 pl-5 doc-prose">
              <li>示例中的 BASE_URL 是网关根地址，路径已包含 /v1 或厂商前缀，不要重复拼接 /v1。模型 ID 请以分组模型广场或当前 Key 的模型目录为准。</li>
              <li>普通 Key 按绑定分组调用；复合 Key 的模型名称可能包含分组前缀，请使用模型目录返回的名称。客户端协议是否开放与上游平台是两个不同条件。</li>
              <li>JSON 请求使用 Content-Type: application/json；文件上传使用 multipart/form-data 并由客户端自动生成 boundary。WebSocket、SDP、音频和文件响应见对应接口。</li>
              <li>密钥仅保存在服务端环境变量中，不要放进公开网页、示例仓库或浏览器 URL。文档页面不会读取你的密钥，也不会自动发送计费请求。</li>
              <li>通用网关拒绝 URL 中的非空 key / api_key 参数；仅 /v1beta 和 /antigravity/v1beta 为 Gemini SDK 兼容 ?key=，仍拒绝 ?api_key=。优先使用请求头，避免密钥出现在 URL 日志中。</li>
              <li>网关响应遵循对应 AI 协议，不套用管理面板的统一 data envelope。SSE 必须完整解析事件；HTTP 200 并不代表流中没有错误。</li>
              <li>排查时保留响应头 X-Request-ID、X-Client-Request-ID 或 X-Sub2API-Request-ID。不要把密钥和完整隐私内容放到公开错误报告。</li>
            </ul>
          </template>

          <template v-else-if="selectedId === 'errors'">
            <h2 class="doc-title">{{ t('apiDocs.errors') }}</h2>
            <p class="doc-prose mb-5">不同协议、上游以及站点错误策略的状态码和错误字段可能不同，请同时检查 HTTP 状态、响应正文和流式终止事件。以下是排查方向，不是强制统一的错误格式。</p>
            <div class="space-y-5"><section v-for="error in commonApiErrors" :key="error.status" class="rounded-xl border border-gray-200 p-4 dark:border-dark-600"><h3 class="mb-2 font-mono text-sm font-semibold text-gray-900 dark:text-white">{{ error.status }} · {{ error.code }}</h3><p class="doc-prose">{{ error.description }}</p></section></div>
            <h3 class="doc-section-title mt-7">异步任务没有立即返回结果</h3>
            <p class="doc-prose">提交成功后保存任务 ID，使用原 Key 轮询对应平台的查询接口。queued / processing / in_progress 等中间态不代表失败。创建返回错误且没有任务 ID 的独立 Video 任务会按当前规则释放预留；网络中断等无法确认的情况可能进入待核对，不能根据一次超时直接判断退款结果。失败或取消后的额度以任务记录中的结算状态为准。</p>
          </template>

          <template v-else-if="selectedId === 'quickstart'">
            <div class="mb-3 inline-flex items-center gap-2 rounded-full bg-primary-50 px-3 py-1 text-xs font-medium text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"><Icon name="book" size="sm" />AI GATEWAY</div>
            <h2 class="doc-title">从一个端点开始接入</h2>
            <p class="doc-prose">在同一站点接入对话、图片、视频、语音及原生厂商协议。先选择 Key 对应的平台和模型，再从目录找到匹配的接口。所有示例仅用于展示请求结构。</p>
            <div class="my-6 grid gap-3 sm:grid-cols-3"><div v-for="(step, index) in quickSteps" :key="step.title" class="rounded-xl border border-gray-200 p-4 dark:border-dark-600"><span class="text-xs font-bold text-primary-600">0{{ index + 1 }}</span><h3 class="my-2 text-sm font-semibold text-gray-900 dark:text-white">{{ step.title }}</h3><p class="text-xs leading-6 text-gray-500 dark:text-dark-300">{{ step.text }}</p></div></div>
            <h3 class="doc-section-title">1. 配置站点地址与密钥</h3>
            <ApiDocCodeBlock :code="apiDocEnvironment(baseUrl)" language="Bash" />
            <h3 class="doc-section-title mt-6">2. 查询当前 Key 可见的模型</h3>
            <ApiDocCodeBlock :code="modelListExample" language="Bash" />
            <h3 class="doc-section-title mt-6">3. 选择协议</h3>
            <div class="grid gap-3 sm:grid-cols-2"><router-link v-for="category in apiDocCategories" :key="category.id" :to="`/docs/${apiDocEndpoints.find(endpoint => endpoint.category === category.id)?.id || 'overview'}`" class="group rounded-xl border border-gray-200 p-4 transition hover:border-primary-300 hover:bg-primary-50/40 dark:border-dark-600 dark:hover:border-primary-700 dark:hover:bg-primary-900/10"><h3 class="mb-2 flex items-center justify-between text-sm font-semibold text-gray-900 dark:text-white">{{ category.label }}<Icon name="arrowRight" size="sm" class="text-gray-400 group-hover:text-primary-500" /></h3><p class="text-xs leading-6 text-gray-500 dark:text-dark-300">{{ category.description }}</p></router-link></div>
            <p class="doc-prose mt-6">视频统一 URL 只统一请求入口，参数仍按账号绑定的上游协议解释；原生视频参数无需改成聊天格式。异步图片和视频请使用对应的任务查询接口，长时间生成不需要保持同一个 HTTP 连接。</p>
            <router-link to="/models" class="mt-5 inline-flex items-center gap-2 text-sm font-medium text-primary-600 dark:text-primary-300">{{ t('apiDocs.models') }}<Icon name="arrowRight" size="sm" /></router-link>
          </template>
          <template v-else><h2 class="doc-title">{{ t('apiDocs.notFound') }}</h2><p class="doc-prose">{{ t('apiDocs.notFoundHint') }}</p><router-link to="/docs" class="btn btn-primary mt-5">{{ t('apiDocs.quickstart') }}</router-link></template>
        </article>

        <aside v-if="selectedEndpoint" class="hidden min-w-0 space-y-3 2xl:sticky 2xl:top-[88px] 2xl:block" :aria-label="t('apiDocs.outline')">
          <h2 class="text-xs font-semibold text-gray-900 dark:text-white">{{ t('apiDocs.outline') }}</h2>
          <button v-for="section in outline" :key="section.id" type="button" class="block border-l-2 border-gray-200 py-1 pl-3 text-left text-xs text-gray-500 hover:border-primary-400 hover:text-primary-600 dark:border-dark-600 dark:text-dark-300" @click="scrollToSection(section.id)">{{ t(section.label) }}</button>
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
import ApiDocCodeBlock from '@/components/api-docs/ApiDocCodeBlock.vue'
import { useClipboard } from '@/composables/useClipboard'
import { initTheme, useTheme } from '@/composables/useTheme'
import { sanitizeUrl } from '@/utils/url'
import { apiDocCategories, apiDocEndpoints, apiDocPlatforms, apiDocVideoProtocols, commonApiErrors, filterApiDocEndpoints, platformLabel, videoProtocolLabel } from '@/content/api-docs'
import { apiDocAuthExample, apiDocBaseUrl, apiDocCurl, apiDocEnvironment, apiDocMarkdown } from '@/content/api-docs/format'

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
const isAuthenticated = computed(() => authStore.isAuthenticated)
const siteName = computed(() => appStore.siteName || 'TokenRouter')
const siteLogo = computed(() => sanitizeUrl(appStore.cachedPublicSettings?.site_logo || appStore.siteLogo || '', { allowRelative: true, allowDataUrl: true }))
const baseUrl = computed(() => apiDocBaseUrl(appStore.apiBaseUrl || '', window.location.origin))
const selectedId = computed(() => typeof route.params.documentId === 'string' && route.params.documentId ? route.params.documentId : 'quickstart')
const selectedEndpoint = computed(() => apiDocEndpoints.find(endpoint => endpoint.id === selectedId.value))
const selectedCategory = computed(() => apiDocCategories.find(category => category.id === selectedEndpoint.value?.category)?.label)
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
const quickSteps = [
  { title: '创建 API Key', text: '选择可用分组和扣费方式，保存站内密钥。' },
  { title: '核对模型与协议', text: '查询可见模型，确认分组已启用目标请求协议。' },
  { title: '提交并读取结果', text: '文本解析普通响应或 SSE；异步任务保存 ID 后轮询。' },
]
const modelListExample = 'curl "$BASE_URL/v1/models" \\\n  -H "Authorization: Bearer $TOKENROUTER_API_KEY"'
const outline = computed(() => [
  { id: 'authorization', label: 'apiDocs.authorization' }, { id: 'parameters', label: 'apiDocs.parameters' },
  { id: 'request', label: 'apiDocs.request' }, { id: 'response', label: 'apiDocs.response' },
  ...(selectedEndpoint.value?.notes?.length ? [{ id: 'notes', label: 'apiDocs.notes' }] : []),
  ...(selectedEndpoint.value?.aliases?.length ? [{ id: 'aliases', label: 'apiDocs.aliases' }] : []),
  { id: 'errors', label: 'apiDocs.errors' },
])

function methodColor(method: string): string {
  if (method === 'GET') return 'text-emerald-600 dark:text-emerald-400'
  if (method === 'DELETE') return 'text-red-600 dark:text-red-400'
  if (method === 'WS') return 'text-violet-600 dark:text-violet-400'
  return 'text-blue-600 dark:text-blue-400'
}
function scrollToSection(id: string) {
  document.getElementById(`doc-${id}`)?.scrollIntoView({ behavior: 'smooth', block: 'start' })
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
})
</script>

<style scoped>
.api-doc-layout { display: grid; grid-template-columns: minmax(0, 1fr); }
@media (min-width: 1024px) { .api-doc-layout { grid-template-columns: 230px minmax(0, 1fr); } }
@media (min-width: 1536px) { .api-doc-layout.has-outline { grid-template-columns: 240px minmax(0, 1fr) 140px; } }
.doc-nav-link { @apply block rounded-lg px-2 py-2 text-xs leading-5 text-gray-600 transition-colors hover:bg-gray-100 dark:text-dark-300 dark:hover:bg-dark-700; }
.doc-nav-active { @apply bg-primary-50 font-medium text-primary-700 dark:bg-primary-900/30 dark:text-primary-300; }
.doc-title { @apply mb-4 text-2xl font-bold tracking-tight text-gray-900 dark:text-white; }
.doc-prose { @apply break-words text-sm leading-7 text-gray-600 dark:text-dark-300; }
.doc-section { @apply mt-8 scroll-mt-20; }
.doc-section-title { @apply mb-3 text-base font-semibold text-gray-900 dark:text-white; }
.doc-table-wrapper { @apply max-w-full overflow-x-auto rounded-xl border border-gray-200 dark:border-dark-600; }
.doc-table { @apply w-full min-w-[560px] border-collapse text-left text-sm; }
.doc-table th { @apply whitespace-nowrap bg-gray-50 px-3 py-3 text-xs font-semibold text-gray-500 dark:bg-dark-800 dark:text-dark-300; }
.doc-table td { @apply border-t border-gray-100 px-3 py-3 align-top text-xs leading-6 text-gray-600 dark:border-dark-700 dark:text-dark-300; overflow-wrap: anywhere; }
</style>
