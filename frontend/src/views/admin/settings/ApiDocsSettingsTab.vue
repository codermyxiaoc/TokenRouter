<template>
  <!-- API 文档目录管理：左侧分类与文档目录（增删改、排序），右侧逐篇编辑标题、分类、可见性与 Markdown -->
  <section class="card" data-testid="api-docs-settings">
    <div class="flex flex-wrap items-start justify-between gap-4 border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <div class="min-w-0">
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('admin.apiDocsSettings.title') }}</h2>
        <!-- 双大括号占位符按纯文本绑定，避免被 Vue 当作插值的结束标记。 -->
        <p class="mt-1 text-sm text-gray-500" v-text="t('admin.apiDocsSettings.description', { placeholder: '{{BASE_URL}}' })"></p>
      </div>
      <div class="flex shrink-0 gap-2">
        <button type="button" class="btn btn-secondary btn-sm" :disabled="loading || busy" @click="reload">
          <Icon name="refresh" size="sm" class="mr-1" />{{ t('common.refresh') }}
        </button>
        <a :href="`/docs/${selectedDocId ?? ''}`" target="_blank" rel="noopener" class="btn btn-secondary btn-sm whitespace-nowrap">
          <Icon name="externalLink" size="sm" class="mr-1" />{{ t('admin.apiDocsSettings.openPage') }}
        </a>
      </div>
    </div>

    <div v-if="loading" class="p-10 text-center text-gray-500" role="status">{{ t('common.loading') }}</div>
    <div v-else class="grid gap-6 p-6 lg:grid-cols-[20rem_minmax(0,1fr)]">
      <!-- 目录 -->
      <aside class="min-w-0 space-y-3" :aria-label="t('admin.apiDocsSettings.catalog')">
        <div class="flex gap-2">
          <button type="button" class="btn btn-secondary btn-sm flex-1" :disabled="busy" data-testid="api-docs-add-category" @click="openCategoryDialog()">
            <Icon name="plus" size="sm" class="mr-1" />{{ t('admin.apiDocsSettings.addCategory') }}
          </button>
          <button type="button" class="btn btn-primary btn-sm flex-1" :disabled="busy" data-testid="api-docs-add-document" @click="openDocumentDialog()">
            <Icon name="plus" size="sm" class="mr-1" />{{ t('admin.apiDocsSettings.addDocument') }}
          </button>
        </div>
        <SearchInput v-model="search" :placeholder="t('admin.apiDocsSettings.search')" />
        <div class="max-h-[44rem] space-y-3 overflow-y-auto overscroll-contain pr-1">
          <div v-for="(category, categoryIndex) in visibleCategories" :key="category.id" class="rounded-lg border border-gray-200 dark:border-dark-600" :data-testid="`api-docs-category-${category.id}`">
            <div class="flex items-center gap-1 border-b border-gray-100 bg-gray-50 px-2 py-1.5 dark:border-dark-700 dark:bg-dark-800">
              <button type="button" class="btn-ghost btn-icon" :aria-label="!expanded[category.id] ? t('admin.apiDocsSettings.expand') : t('admin.apiDocsSettings.collapse')" @click="expanded[category.id] = !expanded[category.id]">
                <Icon :name="!expanded[category.id] ? 'chevronRight' : 'chevronDown'" size="xs" />
              </button>
              <span class="min-w-0 flex-1 truncate text-xs font-semibold" :class="category.hidden ? 'text-gray-400 line-through dark:text-dark-500' : 'text-gray-800 dark:text-dark-100'">{{ category.title }}</span>
              <span v-if="!category.builtin" class="badge badge-primary">{{ t('admin.apiDocsSettings.custom') }}</span>
              <span v-else-if="category.hidden" class="badge badge-gray">{{ t('admin.apiDocsSettings.hidden') }}</span>
              <template v-if="!search">
                <button type="button" class="btn-ghost btn-icon" :disabled="busy || categoryIndex === 0" :aria-label="t('admin.apiDocsSettings.moveUp')" @click="moveCategory(categoryIndex, -1)"><Icon name="arrowUp" size="xs" /></button>
                <button type="button" class="btn-ghost btn-icon" :disabled="busy || categoryIndex === visibleCategories.length - 1" :aria-label="t('admin.apiDocsSettings.moveDown')" @click="moveCategory(categoryIndex, 1)"><Icon name="arrowDown" size="xs" /></button>
              </template>
              <button type="button" class="btn-ghost btn-icon" :disabled="busy" :aria-label="t('admin.apiDocsSettings.editCategory')" :data-testid="`api-docs-edit-category-${category.id}`" @click="openCategoryDialog(category)"><Icon name="edit" size="xs" /></button>
            </div>
            <ul v-show="expanded[category.id]" class="space-y-0.5 p-1.5">
              <li v-for="(doc, docIndex) in category.documents" :key="doc.id" class="group flex items-center gap-1">
                <button type="button" class="doc-row" :class="{ 'doc-row-active': selectedDocId === doc.id }" :data-testid="`api-docs-doc-${doc.id}`" @click="selectDocument(doc.id)">
                  <span v-if="doc.endpoint" class="mr-1.5 font-mono text-[10px] font-bold text-primary-600 dark:text-primary-300">{{ doc.endpoint.method }}</span>
                  <span class="truncate" :class="{ 'text-gray-400 line-through dark:text-dark-500': doc.hidden }">{{ doc.title }}</span>
                  <span v-if="!doc.builtin" class="ml-auto h-1.5 w-1.5 shrink-0 rounded-full bg-primary-500" :title="t('admin.apiDocsSettings.custom')"></span>
                  <span v-else-if="doc.customized" class="ml-auto h-1.5 w-1.5 shrink-0 rounded-full bg-amber-500" :title="t('admin.apiDocsSettings.customized')"></span>
                </button>
                <template v-if="!search">
                  <button type="button" class="btn-ghost btn-icon opacity-0 group-hover:opacity-100 focus:opacity-100" :disabled="busy || docIndex === 0" :aria-label="t('admin.apiDocsSettings.moveUp')" @click="moveDocument(category.id, docIndex, -1)"><Icon name="arrowUp" size="xs" /></button>
                  <button type="button" class="btn-ghost btn-icon opacity-0 group-hover:opacity-100 focus:opacity-100" :disabled="busy || docIndex === category.documents.length - 1" :aria-label="t('admin.apiDocsSettings.moveDown')" @click="moveDocument(category.id, docIndex, 1)"><Icon name="arrowDown" size="xs" /></button>
                </template>
              </li>
              <li v-if="!category.documents.length" class="px-2 py-2 text-xs text-gray-400">{{ t('admin.apiDocsSettings.emptyCategory') }}</li>
            </ul>
          </div>
          <p v-if="!visibleCategories.length" class="px-2 text-sm text-gray-500">{{ t('admin.apiDocsSettings.noResults') }}</p>
        </div>
        <p class="flex flex-wrap gap-3 text-xs text-gray-500">
          <span class="inline-flex items-center gap-1"><span class="h-1.5 w-1.5 rounded-full bg-amber-500"></span>{{ t('admin.apiDocsSettings.customized') }}</span>
          <span class="inline-flex items-center gap-1"><span class="h-1.5 w-1.5 rounded-full bg-primary-500"></span>{{ t('admin.apiDocsSettings.custom') }}</span>
        </p>
      </aside>

      <!-- 编辑器 -->
      <div class="min-w-0 space-y-5">
        <p v-if="error" role="alert" class="rounded-lg bg-red-50 px-4 py-3 text-sm text-red-600 dark:bg-red-900/20 dark:text-red-400">{{ error }}</p>
        <div v-if="!selectedDoc" class="rounded-lg border border-dashed border-gray-300 p-10 text-center text-sm text-gray-500 dark:border-dark-600">{{ t('admin.apiDocsSettings.selectHint') }}</div>
        <div v-else-if="docLoading" class="p-10 text-center text-gray-500" role="status">{{ t('common.loading') }}</div>
        <template v-else-if="form">
          <div class="flex flex-wrap items-center gap-2">
            <span :class="['badge', selectedDoc.builtin ? (selectedDoc.customized ? 'badge-warning' : 'badge-gray') : 'badge-primary']" data-testid="api-docs-state">
              {{ selectedDoc.builtin ? (selectedDoc.customized ? t('admin.apiDocsSettings.customized') : t('admin.apiDocsSettings.builtin')) : t('admin.apiDocsSettings.custom') }}
            </span>
            <code class="rounded bg-gray-100 px-2 py-0.5 text-xs text-gray-600 dark:bg-dark-700 dark:text-dark-300">/docs/{{ selectedDoc.id }}</code>
          </div>
          <div class="grid gap-4 md:grid-cols-[minmax(0,1fr)_16rem]">
            <div>
              <label class="input-label" for="api-doc-title">{{ t('admin.apiDocsSettings.docTitle') }}</label>
              <input id="api-doc-title" v-model="form.title" type="text" maxlength="200" class="input w-full" :placeholder="selectedDoc.defaultTitle" data-testid="api-docs-title" @input="dirty = true" />
            </div>
            <div>
              <label class="input-label">{{ t('admin.apiDocsSettings.category') }}</label>
              <Select v-model="form.categoryId" :options="categoryOptions" data-testid="api-docs-category" @update:model-value="dirty = true" />
            </div>
          </div>
          <div class="flex items-center justify-between gap-4 rounded-lg border border-gray-200 px-4 py-3 dark:border-dark-600">
            <div>
              <label for="api-doc-visible" class="input-label !mb-0">{{ t('admin.apiDocsSettings.visible') }}</label>
              <p class="mt-1 text-xs text-gray-500">{{ t('admin.apiDocsSettings.visibleHint') }}</p>
            </div>
            <Toggle id="api-doc-visible" :model-value="!form.hidden" :aria-label="t('admin.apiDocsSettings.visible')" @update:model-value="value => { form!.hidden = !value; dirty = true }" />
          </div>
          <div class="grid gap-4 xl:grid-cols-2">
            <div class="min-w-0">
              <label class="input-label" for="api-doc-markdown">{{ t('admin.apiDocsSettings.markdown') }}</label>
              <textarea id="api-doc-markdown" v-model="form.markdown" class="input min-h-[32rem] w-full font-mono text-xs leading-5" spellcheck="false" data-testid="api-docs-editor" @input="dirty = true"></textarea>
            </div>
            <div class="min-w-0">
              <span class="input-label">{{ t('admin.apiDocsSettings.preview') }}</span>
              <div class="max-h-[32rem] min-h-[32rem] overflow-y-auto rounded-lg border border-gray-200 p-4 dark:border-dark-600" data-testid="api-docs-preview">
                <ApiDocMarkdown :markdown="previewMarkdown" />
              </div>
            </div>
          </div>
          <div class="flex flex-wrap items-center justify-between gap-3 border-t border-gray-100 pt-5 dark:border-dark-700">
            <div class="flex flex-wrap gap-2">
              <button v-if="selectedDoc.builtin" type="button" class="btn btn-secondary" :disabled="busy || !selectedDoc.customized" data-testid="api-docs-reset" @click="confirmAction = 'resetDocument'">{{ t('admin.apiDocsSettings.reset') }}</button>
              <button v-else type="button" class="btn btn-danger" :disabled="busy" data-testid="api-docs-delete" @click="confirmAction = 'deleteDocument'">
                <Icon name="trash" size="sm" class="mr-1" />{{ t('common.delete') }}
              </button>
            </div>
            <div class="flex items-center gap-3">
              <span v-if="dirty" class="text-sm text-amber-600">{{ t('admin.apiDocsSettings.unsaved') }}</span>
              <button type="button" class="btn btn-primary" :disabled="busy || !dirty || !formValid" data-testid="api-docs-save" @click="saveDocument">
                {{ busy ? t('common.processing') : t('common.save') }}
              </button>
            </div>
          </div>
        </template>
      </div>
    </div>

    <!-- 新增文档 -->
    <BaseDialog :show="documentDialog.show" :title="t('admin.apiDocsSettings.addDocument')" width="narrow" @close="documentDialog.show = false">
      <form id="api-doc-create-form" class="space-y-4" @submit.prevent="createDocument">
        <div>
          <label class="input-label" for="api-doc-new-title">{{ t('admin.apiDocsSettings.docTitle') }}</label>
          <input id="api-doc-new-title" v-model="documentDialog.title" type="text" maxlength="200" class="input w-full" data-testid="api-docs-new-title" @input="suggestDocumentId" />
        </div>
        <div>
          <label class="input-label" for="api-doc-new-id">{{ t('admin.apiDocsSettings.docId') }}</label>
          <input id="api-doc-new-id" v-model="documentDialog.id" type="text" maxlength="128" class="input w-full font-mono" data-testid="api-docs-new-id" @input="documentDialog.idTouched = true" />
          <p class="mt-1 text-xs text-gray-500">{{ t('admin.apiDocsSettings.docIdHint') }}</p>
        </div>
        <div>
          <label class="input-label">{{ t('admin.apiDocsSettings.category') }}</label>
          <Select v-model="documentDialog.categoryId" :options="categoryOptions" />
        </div>
        <p v-if="documentDialog.error" role="alert" class="text-sm text-red-600">{{ documentDialog.error }}</p>
      </form>
      <template #footer>
        <div class="flex justify-end gap-3">
          <button type="button" class="btn btn-secondary" :disabled="busy" @click="documentDialog.show = false">{{ t('common.cancel') }}</button>
          <button type="submit" form="api-doc-create-form" class="btn btn-primary" :disabled="busy" data-testid="api-docs-create">{{ busy ? t('common.processing') : t('common.create') }}</button>
        </div>
      </template>
    </BaseDialog>

    <!-- 新增 / 编辑分类 -->
    <BaseDialog :show="categoryDialog.show" :title="categoryDialog.target ? t('admin.apiDocsSettings.editCategory') : t('admin.apiDocsSettings.addCategory')" width="narrow" @close="categoryDialog.show = false">
      <form id="api-doc-category-form" class="space-y-4" @submit.prevent="saveCategory">
        <div>
          <label class="input-label" for="api-doc-category-title">{{ t('admin.apiDocsSettings.categoryTitle') }}</label>
          <input id="api-doc-category-title" v-model="categoryDialog.title" type="text" maxlength="200" class="input w-full" :placeholder="categoryDialog.target?.defaultTitle" data-testid="api-docs-category-title" @input="suggestCategoryId" />
        </div>
        <div v-if="!categoryDialog.target">
          <label class="input-label" for="api-doc-category-id">{{ t('admin.apiDocsSettings.categoryId') }}</label>
          <input id="api-doc-category-id" v-model="categoryDialog.id" type="text" maxlength="128" class="input w-full font-mono" data-testid="api-docs-category-id" @input="categoryDialog.idTouched = true" />
          <p class="mt-1 text-xs text-gray-500">{{ t('admin.apiDocsSettings.docIdHint') }}</p>
        </div>
        <div v-else class="flex items-center justify-between gap-4">
          <div>
            <label for="api-doc-category-visible" class="input-label !mb-0">{{ t('admin.apiDocsSettings.visible') }}</label>
            <p class="mt-1 text-xs text-gray-500">{{ t('admin.apiDocsSettings.categoryVisibleHint') }}</p>
          </div>
          <Toggle id="api-doc-category-visible" :model-value="!categoryDialog.hidden" :aria-label="t('admin.apiDocsSettings.visible')" @update:model-value="value => { categoryDialog.hidden = !value }" />
        </div>
        <p v-if="categoryDialog.error" role="alert" class="text-sm text-red-600">{{ categoryDialog.error }}</p>
      </form>
      <template #footer>
        <div class="flex flex-wrap justify-between gap-3">
          <div>
            <button v-if="categoryDialog.target && !categoryDialog.target.builtin" type="button" class="btn btn-danger" :disabled="busy" data-testid="api-docs-delete-category" @click="confirmAction = 'deleteCategory'">{{ t('common.delete') }}</button>
            <button v-else-if="categoryDialog.target?.overlay?.version" type="button" class="btn btn-secondary" :disabled="busy" @click="confirmAction = 'resetCategory'">{{ t('admin.apiDocsSettings.reset') }}</button>
          </div>
          <div class="flex gap-3">
            <button type="button" class="btn btn-secondary" :disabled="busy" @click="categoryDialog.show = false">{{ t('common.cancel') }}</button>
            <button type="submit" form="api-doc-category-form" class="btn btn-primary" :disabled="busy" data-testid="api-docs-save-category">{{ busy ? t('common.processing') : t('common.save') }}</button>
          </div>
        </div>
      </template>
    </BaseDialog>

    <ConfirmDialog
      :show="!!confirmAction"
      :title="confirmAction ? t(`admin.apiDocsSettings.confirm.${confirmAction}.title`) : ''"
      :message="confirmAction ? t(`admin.apiDocsSettings.confirm.${confirmAction}.message`) : ''"
      :danger="confirmAction === 'deleteDocument' || confirmAction === 'deleteCategory'"
      :loading="busy"
      @confirm="runConfirmedAction"
      @cancel="confirmAction = null"
    />
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import Toggle from '@/components/common/Toggle.vue'
import SearchInput from '@/components/common/SearchInput.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import ApiDocMarkdown from '@/components/api-docs/ApiDocMarkdown.vue'
import { apiDocsAPI, type ApiDocCatalogOverlay } from '@/api/apiDocs'
import { useAppStore } from '@/stores/app'
import { extractApiErrorCode, extractApiErrorMessage } from '@/utils/apiError'
import { apiDocBaseUrl } from '@/content/api-docs/format'
import { defaultApiDocMarkdown } from '@/content/api-docs/markdown'
import { apiDocCategoryDocuments, buildApiDocCatalog, isBuiltinApiDocId, type ApiDocCatalogCategory } from '@/content/api-docs/catalog'

const { t } = useI18n()
const app = useAppStore()
const ID_PATTERN = /^[a-z0-9][a-z0-9_-]{0,127}$/

const loading = ref(false)
const busy = ref(false)
const error = ref('')
const search = ref('')
// 目录默认全部折叠，避免分类多时首屏把右侧编辑区挤到很远处；展开状态按分类记忆。
const expanded = reactive<Record<string, boolean>>({})
const overlay = ref<ApiDocCatalogOverlay>({ categories: [], documents: [] })
const baseUrl = computed(() => apiDocBaseUrl(app.apiBaseUrl || '', window.location.origin))
// 管理端显示全部条目（含隐藏），隐藏项以删除线标出。
const catalog = computed(() => buildApiDocCatalog(overlay.value, true))
const categoryOptions = computed(() => catalog.value.categories.map(category => ({ value: category.id, label: category.title })))
const visibleCategories = computed(() => {
  const term = search.value.trim().toLocaleLowerCase()
  return catalog.value.categories.map(category => ({
    ...category,
    documents: apiDocCategoryDocuments(catalog.value, category.id)
      .filter(doc => !term || doc.title.toLocaleLowerCase().includes(term) || doc.id.includes(term) || doc.endpoint?.path.toLocaleLowerCase().includes(term)),
  })).filter(category => !term || category.documents.length || category.title.toLocaleLowerCase().includes(term))
})

// ---- 文档编辑 ----
interface DocForm { title: string; categoryId: string; hidden: boolean; markdown: string; version: number }
const selectedDocId = ref<string | null>('quickstart')
const selectedDoc = computed(() => catalog.value.documents.find(doc => doc.id === selectedDocId.value))
const form = ref<DocForm | null>(null)
const docLoading = ref(false)
const dirty = ref(false)
const formValid = computed(() => !!form.value && !!form.value.categoryId && (selectedDoc.value?.builtin || (!!form.value.title.trim() && !!form.value.markdown.trim())))
// 预览与公开页一致地替换 {{BASE_URL}} 占位符。
const previewMarkdown = computed(() => (form.value?.markdown ?? '').split('{{BASE_URL}}').join(baseUrl.value))

function builtinMarkdown(id: string): string {
  return defaultApiDocMarkdown(id, baseUrl.value) ?? ''
}

// 载入单篇：有改写正文时向服务端读取，否则使用内置正文；version 取叠加记录（无则 0 表示新建）。
let docRequest = 0
async function loadForm() {
  const doc = selectedDoc.value
  const request = ++docRequest
  dirty.value = false
  if (!doc) {
    form.value = null
    return
  }
  let markdown = doc.builtin ? builtinMarkdown(doc.id) : ''
  let version = doc.overlay?.version ?? 0
  if (doc.hasMarkdown) {
    docLoading.value = true
    try {
      const entry = await apiDocsAPI.getAdminDocument(doc.id)
      if (request !== docRequest) return
      markdown = entry.markdown ?? markdown
      version = entry.version
    } catch (err) {
      if (request !== docRequest) return
      error.value = extractApiErrorMessage(err, t('admin.apiDocsSettings.loadFailed'))
    } finally {
      if (request === docRequest) docLoading.value = false
    }
  }
  form.value = { title: doc.title, categoryId: doc.categoryId, hidden: doc.hidden, markdown, version }
}

function confirmDiscard(): boolean {
  return !dirty.value || window.confirm(t('admin.apiDocsSettings.discardConfirm'))
}

function selectDocument(id: string) {
  if (id === selectedDocId.value || !confirmDiscard()) return
  error.value = ''
  selectedDocId.value = id
  void loadForm()
}

async function reloadCatalog() {
  overlay.value = await apiDocsAPI.getAdminCatalog()
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    await reloadCatalog()
    await loadForm()
  } catch (err) {
    error.value = extractApiErrorMessage(err, t('admin.apiDocsSettings.loadFailed'))
  } finally {
    loading.value = false
  }
}

function reload() {
  if (!confirmDiscard()) return
  void load()
}

// 统一处理并发冲突：刷新目录并保留当前草稿，管理员核对后再次保存即以最新版本为起点。
async function handleFailure(err: unknown, fallback: string) {
  const code = extractApiErrorCode(err)
  if (code === 'API_DOC_CONFLICT') {
    error.value = t('admin.apiDocsSettings.conflict')
    try {
      await reloadCatalog()
      if (form.value && selectedDoc.value) form.value.version = selectedDoc.value.overlay?.version ?? 0
    } catch {
      // 刷新失败时保留原状态，下次保存仍会被服务端拦截。
    }
    return
  }
  error.value = code === 'API_DOC_CATEGORY_NOT_EMPTY' ? t('admin.apiDocsSettings.categoryNotEmpty') : extractApiErrorMessage(err, fallback)
}

// 内置文档只保存与内置不同的字段（相同则存 null 继续跟随内置版本）；自定义文档保存完整内容。
async function saveDocument() {
  const doc = selectedDoc.value
  if (!doc || !form.value || !formValid.value || busy.value) return
  busy.value = true
  error.value = ''
  const submitted = { ...form.value }
  const title = submitted.title.trim()
  const markdown = submitted.markdown.trim() ? submitted.markdown : ''
  try {
    const saved = await apiDocsAPI.saveDocument(doc.id, doc.builtin
      ? {
          title: title && title !== doc.defaultTitle ? title : null,
          category_id: submitted.categoryId !== doc.defaultCategoryId ? submitted.categoryId : null,
          markdown: markdown && markdown !== builtinMarkdown(doc.id) ? markdown : null,
          hidden: submitted.hidden, custom: false, version: submitted.version,
        }
      : { title, category_id: submitted.categoryId, markdown, hidden: submitted.hidden, custom: true, version: submitted.version })
    await reloadCatalog()
    // 保存期间继续输入的内容不能被响应覆盖，只更新版本号。
    if (form.value && selectedDocId.value === doc.id) {
      form.value.version = saved.version
      dirty.value = form.value.markdown !== submitted.markdown || form.value.title !== submitted.title
    }
    app.showSuccess(t('admin.apiDocsSettings.saved'))
  } catch (err) {
    await handleFailure(err, t('admin.apiDocsSettings.saveFailed'))
  } finally {
    busy.value = false
  }
}

// ---- 新增文档 ----
const documentDialog = reactive({ show: false, title: '', id: '', idTouched: false, categoryId: 'guides', error: '' })

function slugify(value: string): string {
  return value.toLocaleLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 64)
}

function uniqueSlug(base: string, prefix: string): string {
  const used = new Set([...catalog.value.documents.map(doc => doc.id), ...catalog.value.categories.map(category => category.id)])
  const root = base || `${prefix}-${Date.now().toString(36)}`
  let id = root
  for (let n = 2; used.has(id) || isBuiltinApiDocId(id); n++) id = `${root}-${n}`
  return id
}

function openDocumentDialog() {
  if (!confirmDiscard()) return
  Object.assign(documentDialog, { show: true, title: '', id: uniqueSlug('', 'doc'), idTouched: false, error: '',
    categoryId: selectedDoc.value?.categoryId ?? catalog.value.categories[0]?.id ?? 'guides' })
}

function suggestDocumentId() {
  if (!documentDialog.idTouched) documentDialog.id = uniqueSlug(slugify(documentDialog.title), 'doc')
}

function validateNewId(id: string): string {
  if (!ID_PATTERN.test(id)) return t('admin.apiDocsSettings.idInvalid')
  if (isBuiltinApiDocId(id) || catalog.value.documents.some(doc => doc.id === id) || catalog.value.categories.some(category => category.id === id)) return t('admin.apiDocsSettings.idTaken')
  return ''
}

async function createDocument() {
  const title = documentDialog.title.trim()
  const id = documentDialog.id.trim()
  documentDialog.error = !title ? t('admin.apiDocsSettings.titleRequired') : validateNewId(id)
  if (documentDialog.error || busy.value) return
  busy.value = true
  try {
    await apiDocsAPI.saveDocument(id, { title, category_id: documentDialog.categoryId, markdown: `# ${title}\n`, hidden: false, custom: true, version: 0 })
    await reloadCatalog()
    documentDialog.show = false
    selectedDocId.value = id
    await loadForm()
    app.showSuccess(t('admin.apiDocsSettings.created'))
  } catch (err) {
    documentDialog.error = extractApiErrorCode(err) === 'API_DOC_CONFLICT' ? t('admin.apiDocsSettings.idTaken') : extractApiErrorMessage(err, t('admin.apiDocsSettings.saveFailed'))
  } finally {
    busy.value = false
  }
}

// ---- 分类 ----
const categoryDialog = reactive({ show: false, target: null as ApiDocCatalogCategory | null, title: '', id: '', idTouched: false, hidden: false, error: '' })

function openCategoryDialog(category?: ApiDocCatalogCategory) {
  Object.assign(categoryDialog, {
    show: true, target: category ?? null, error: '', idTouched: false,
    title: category ? category.title : '', id: category ? category.id : uniqueSlug('', 'category'), hidden: category?.hidden ?? false,
  })
}

function suggestCategoryId() {
  if (!categoryDialog.target && !categoryDialog.idTouched) categoryDialog.id = uniqueSlug(slugify(categoryDialog.title), 'category')
}

async function saveCategory() {
  const target = categoryDialog.target
  const title = categoryDialog.title.trim()
  if (!target) {
    categoryDialog.error = !title ? t('admin.apiDocsSettings.titleRequired') : validateNewId(categoryDialog.id.trim())
  } else {
    categoryDialog.error = !target.builtin && !title ? t('admin.apiDocsSettings.titleRequired') : ''
  }
  if (categoryDialog.error || busy.value) return
  busy.value = true
  try {
    const id = target?.id ?? categoryDialog.id.trim()
    await apiDocsAPI.saveCategory(id, {
      // 内置分类与内置标题相同则保存 null，继续跟随内置名称。
      title: target?.builtin && (!title || title === target.defaultTitle) ? null : title,
      hidden: categoryDialog.hidden, custom: !target?.builtin, version: target?.overlay?.version ?? 0,
    })
    await reloadCatalog()
    categoryDialog.show = false
    app.showSuccess(t('admin.apiDocsSettings.saved'))
  } catch (err) {
    if (extractApiErrorCode(err) === 'API_DOC_CONFLICT') {
      categoryDialog.error = t('admin.apiDocsSettings.conflict')
      // 冲突后必须把弹窗的编辑起点同步到最新版本，否则重试仍提交旧版本，会一直 409。
      try {
        await reloadCatalog()
        if (categoryDialog.target) {
          categoryDialog.target = catalog.value.categories.find(category => category.id === categoryDialog.target?.id) ?? null
        }
      } catch {
        // 刷新失败时保留原状态，下次保存仍会被服务端拦截。
      }
    } else {
      categoryDialog.error = extractApiErrorMessage(err, t('admin.apiDocsSettings.saveFailed'))
    }
  } finally {
    busy.value = false
  }
}

// ---- 排序：上下移动后整体提交该层级的新顺序 ----
async function submitOrder(order: { categories?: string[]; documents?: Record<string, string[]> }) {
  busy.value = true
  error.value = ''
  try {
    await apiDocsAPI.reorder(order)
    await reloadCatalog()
  } catch (err) {
    await handleFailure(err, t('admin.apiDocsSettings.saveFailed'))
  } finally {
    busy.value = false
  }
}

function moved<T>(items: T[], index: number, delta: number): T[] {
  const next = [...items]
  const [item] = next.splice(index, 1)
  next.splice(index + delta, 0, item)
  return next
}

function moveCategory(index: number, delta: number) {
  void submitOrder({ categories: moved(catalog.value.categories.map(category => category.id), index, delta) })
}

function moveDocument(categoryId: string, index: number, delta: number) {
  const ids = apiDocCategoryDocuments(catalog.value, categoryId).map(doc => doc.id)
  void submitOrder({ documents: { [categoryId]: moved(ids, index, delta) } })
}

// ---- 删除 / 恢复默认（二次确认） ----
type ConfirmAction = 'deleteDocument' | 'resetDocument' | 'deleteCategory' | 'resetCategory'
const confirmAction = ref<ConfirmAction | null>(null)

async function runConfirmedAction() {
  const action = confirmAction.value
  if (!action || busy.value) return
  busy.value = true
  error.value = ''
  try {
    if (action === 'deleteDocument' || action === 'resetDocument') {
      const doc = selectedDoc.value
      if (!doc?.overlay?.version) return
      await apiDocsAPI.deleteDocument(doc.id, doc.overlay.version)
      await reloadCatalog()
      if (action === 'deleteDocument') selectedDocId.value = catalog.value.documents[0]?.id ?? null
      await loadForm()
    } else {
      const category = categoryDialog.target
      if (!category?.overlay?.version) return
      await apiDocsAPI.deleteCategory(category.id, category.overlay.version)
      await reloadCatalog()
      categoryDialog.show = false
    }
    app.showSuccess(t('admin.apiDocsSettings.saved'))
  } catch (err) {
    if (action === 'deleteCategory' || action === 'resetCategory') {
      categoryDialog.error = extractApiErrorCode(err) === 'API_DOC_CATEGORY_NOT_EMPTY' ? t('admin.apiDocsSettings.categoryNotEmpty') : extractApiErrorMessage(err, t('admin.apiDocsSettings.saveFailed'))
    } else {
      await handleFailure(err, t('admin.apiDocsSettings.saveFailed'))
    }
  } finally {
    busy.value = false
    confirmAction.value = null
  }
}

onMounted(load)
</script>

<style scoped>
.doc-row { @apply flex min-w-0 flex-1 items-center rounded-md px-2 py-1.5 text-left text-xs leading-5 text-gray-600 transition-colors hover:bg-gray-100 dark:text-dark-300 dark:hover:bg-dark-700; }
.doc-row-active { @apply bg-primary-50 font-medium text-primary-700 dark:bg-primary-900/30 dark:text-primary-300; }
</style>
