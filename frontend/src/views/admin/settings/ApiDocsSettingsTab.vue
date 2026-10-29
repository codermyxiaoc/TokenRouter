<template>
  <!-- API 文档编辑：逐篇改写 Markdown，左侧编辑右侧实时预览；未改写的文档使用内置版本 -->
  <section class="card" data-testid="api-docs-settings">
    <div class="flex items-start justify-between gap-4 border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <div>
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('admin.apiDocsSettings.title') }}</h2>
        <!-- 双大括号占位符按纯文本绑定，避免被 Vue 当作插值的结束标记。 -->
        <p class="mt-1 text-sm text-gray-500" v-text="t('admin.apiDocsSettings.description', { placeholder: '{{BASE_URL}}' })"></p>
      </div>
      <a :href="`/docs/${selectedId}`" target="_blank" rel="noopener" class="btn btn-secondary btn-sm shrink-0 whitespace-nowrap">
        <Icon name="book" size="sm" class="mr-1" />{{ t('admin.apiDocsSettings.openPage') }}
      </a>
    </div>
    <div v-if="loading" class="p-10 text-center text-gray-500" role="status">{{ t('common.loading') }}</div>
    <div v-else class="space-y-6 p-6">
      <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
      <div class="flex flex-wrap items-end gap-3">
        <div class="min-w-[18rem] flex-1">
          <label class="input-label">{{ t('admin.apiDocsSettings.document') }}</label>
          <Select v-model="selectedId" :options="documentOptions" searchable data-testid="api-docs-document" />
        </div>
        <span :class="['badge', customized ? 'badge-warning' : 'badge-gray']" data-testid="api-docs-state">
          {{ customized ? t('admin.apiDocsSettings.customized') : t('admin.apiDocsSettings.builtin') }}
        </span>
        <button type="button" class="btn btn-secondary" :disabled="!customized || saving" data-testid="api-docs-reset" @click="resetDocument">{{ t('admin.apiDocsSettings.reset') }}</button>
      </div>
      <div class="grid gap-6 xl:grid-cols-2">
        <div>
          <label class="input-label">{{ t('admin.apiDocsSettings.markdown') }}</label>
          <textarea v-model="draft" class="input min-h-[36rem] w-full font-mono text-xs leading-5" spellcheck="false" data-testid="api-docs-editor" @input="markDirty"></textarea>
        </div>
        <div class="min-w-0">
          <label class="input-label">{{ t('admin.apiDocsSettings.preview') }}</label>
          <div class="max-h-[36rem] min-h-[36rem] overflow-y-auto rounded-lg border border-gray-200 p-4 dark:border-dark-600" data-testid="api-docs-preview">
            <ApiDocMarkdown :markdown="previewMarkdown" />
          </div>
        </div>
      </div>
      <div class="flex items-center justify-end gap-3 border-t border-gray-100 pt-6 dark:border-dark-700">
        <span v-if="dirty" class="text-sm text-amber-600">{{ t('admin.apiDocsSettings.unsaved') }}</span>
        <button type="button" class="btn btn-primary" :disabled="saving || !dirty" data-testid="api-docs-save" @click="save">
          {{ saving ? t('common.processing') : t('common.save') }}
        </button>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import ApiDocMarkdown from '@/components/api-docs/ApiDocMarkdown.vue'
import { apiDocsAPI } from '@/api/apiDocs'
import { useAppStore } from '@/stores/app'
import { extractApiErrorCode, extractApiErrorMessage } from '@/utils/apiError'
import { apiDocBaseUrl } from '@/content/api-docs/format'
import { apiDocDocuments, defaultApiDocMarkdown } from '@/content/api-docs/markdown'

const { t } = useI18n()
const app = useAppStore()
const loading = ref(false)
const saving = ref(false)
const error = ref('')
// 已保存的改写；draft 为当前文档编辑中的正文，dirty 表示尚未保存。
const overrides = ref<Record<string, string>>({})
const selectedId = ref<string | number | boolean | null>('quickstart')
const draft = ref('')
const dirty = ref(false)
const baseUrl = computed(() => apiDocBaseUrl(app.apiBaseUrl || '', window.location.origin))
const documents = apiDocDocuments()
const documentOptions = computed(() => documents.map(doc => ({
  value: doc.id,
  label: `${doc.group ? `[${doc.group}] ` : ''}${doc.title}${overrides.value[doc.id] ? ` · ${t('admin.apiDocsSettings.customized')}` : ''}`,
})))
const currentId = computed(() => String(selectedId.value ?? 'quickstart'))
const customized = computed(() => !!overrides.value[currentId.value])
// 预览与公开页一致地替换 {{BASE_URL}} 占位符。
const previewMarkdown = computed(() => draft.value.split('{{BASE_URL}}').join(baseUrl.value))

function markDirty() {
  dirty.value = true
}

// 切换文档时载入已保存的改写或内置默认；有未保存修改时先确认。
function loadDraft() {
  draft.value = overrides.value[currentId.value] ?? defaultApiDocMarkdown(currentId.value, baseUrl.value) ?? ''
  dirty.value = false
}

let previousId = currentId.value
// 取消切换时回退选择，回退本身不再触发确认。
let reverting = false
watch(selectedId, () => {
  if (reverting) {
    reverting = false
    return
  }
  if (dirty.value && !window.confirm(t('admin.apiDocsSettings.discardConfirm'))) {
    reverting = true
    selectedId.value = previousId
    return
  }
  previousId = currentId.value
  loadDraft()
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    overrides.value = await apiDocsAPI.getOverrides()
    loadDraft()
  } catch (err) {
    error.value = extractApiErrorMessage(err, t('admin.apiDocsSettings.loadFailed'))
  } finally {
    loading.value = false
  }
}

// 与内置正文完全一致时视为未改写，避免内置文档更新后被旧副本覆盖。
// 只提交当前文档并附带编辑起点：他人改了别的文档不会被覆盖，改了同一文档则提示冲突。
async function persist(id: string, markdown: string) {
  saving.value = true
  error.value = ''
  // 记录提交时的正文；保存期间继续输入的内容不能被响应后的重新载入覆盖。
  const submitted = draft.value
  try {
    overrides.value = await apiDocsAPI.patchOverrides({ [id]: { markdown, base: overrides.value[id] ?? '' } })
    if (currentId.value === id && draft.value === submitted) loadDraft()
    app.showSuccess(t('admin.apiDocsSettings.saved'))
  } catch (err) {
    if (extractApiErrorCode(err) === 'API_DOC_CONFLICT') {
      // 保留本地草稿，刷新已保存版本；管理员确认后再次保存即以最新版本为起点。
      error.value = t('admin.apiDocsSettings.conflict')
      try {
        overrides.value = await apiDocsAPI.getOverrides()
      } catch {
        // 刷新失败时保留原状态，下次保存仍会被服务端拦截。
      }
    } else {
      error.value = extractApiErrorMessage(err, t('admin.apiDocsSettings.saveFailed'))
    }
  } finally {
    saving.value = false
  }
}

function save() {
  const builtin = defaultApiDocMarkdown(currentId.value, baseUrl.value) ?? ''
  const markdown = draft.value.trim() && draft.value !== builtin ? draft.value : ''
  void persist(currentId.value, markdown)
}

function resetDocument() {
  if (!window.confirm(t('admin.apiDocsSettings.resetConfirm'))) return
  void persist(currentId.value, '')
}

onMounted(load)
</script>
