<template>
  <AppLayout>
    <div data-testid="video-protocol-profiles-view">
    <!-- 与其它后台列表页一致：顶部筛选与操作栏，下方共享数据表；页面标题由 AppLayout 展示 -->
    <TablePageLayout>
      <template #filters>
        <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div class="flex min-w-0 flex-1 items-center gap-3">
            <div class="min-w-0 flex-1 sm:w-64 sm:flex-none">
              <input v-model="searchQuery" type="text" class="input" :placeholder="t('admin.videoProtocolProfiles.searchPlaceholder')" data-testid="video-profiles-search" />
            </div>
            <Select v-model="statusFilter" :options="statusFilterOptions" class="w-32 shrink-0 sm:w-40" data-testid="video-profiles-status-filter" />
          </div>
          <div class="flex shrink-0 items-center justify-end gap-2">
            <button type="button" class="btn btn-secondary h-9 w-9 shrink-0 p-0" :disabled="loading" :title="t('common.refresh')" data-testid="video-profiles-refresh" @click="loadProfiles">
              <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
            </button>
            <button type="button" class="btn btn-secondary h-9 shrink-0 whitespace-nowrap" data-testid="video-profiles-ai-open" @click="openAIDialog()">
              <Icon name="sparkles" size="md" class="mr-1" />{{ t('admin.videoProtocolProfiles.ai.open') }}
            </button>
            <button type="button" class="btn btn-primary h-9 shrink-0 whitespace-nowrap" data-testid="video-profiles-create" @click="openEditor()">
              <Icon name="plus" size="md" class="mr-1" />{{ t('admin.videoProtocolProfiles.create') }}
            </button>
          </div>
        </div>
      </template>

      <template #table>
        <!-- 每个版本一行，按键名与版本倒序排列；键名列给出账号绑定写法与整模板删除 -->
        <DataTable :columns="columns" :data="rows" :loading="loading" row-key="id">
          <template #cell-profile_key="{ row }">
            <div class="min-w-0" :data-testid="`video-profile-group-${row.profile_key}`">
              <div class="flex items-center gap-2">
                <span class="truncate font-mono font-medium text-gray-900 dark:text-white">{{ row.profile_key }}</span>
                <button v-if="row.firstOfKey" type="button" class="rounded p-0.5 text-gray-400 transition-colors hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-900/20 dark:hover:text-red-400" :disabled="busyStatus" :title="t('admin.videoProtocolProfiles.deleteTemplate')" :data-testid="`video-profile-delete-group-${row.profile_key}`" @click="askDelete(row.profile_key)">
                  <Icon name="trash" size="xs" />
                </button>
              </div>
              <div class="mt-1 font-mono text-xs text-gray-500 dark:text-dark-400">profile:{{ row.profile_key }}</div>
              <div v-if="row.firstOfKey && !row.keyActiveVersion" class="mt-0.5 text-xs text-amber-600 dark:text-amber-400">{{ t('admin.videoProtocolProfiles.noActiveVersion') }}</div>
            </div>
          </template>

          <template #cell-version="{ row }">
            <span class="font-mono" :data-testid="`video-profile-row-${row.profile_key}-${row.version}`">v{{ row.version }}</span>
          </template>

          <template #cell-name="{ value }">
            <span class="block max-w-[16rem] truncate" :title="value">{{ value }}</span>
          </template>

          <template #cell-status="{ value }">
            <span :class="['badge', value === 'active' ? 'badge-success' : value === 'draft' ? 'badge-warning' : 'badge-gray']">
              {{ t(`admin.videoProtocolProfiles.statuses.${value}`) }}
            </span>
          </template>

          <template #cell-created_at="{ value }">
            <span class="text-sm text-gray-500 dark:text-dark-400">{{ formatDateTime(value) }}</span>
          </template>

          <template #cell-spec_hash="{ value }">
            <span class="font-mono text-xs text-gray-500 dark:text-dark-400" :title="value">{{ String(value).slice(0, 12) }}</span>
          </template>

          <template #cell-actions="{ row }">
            <div class="flex items-center space-x-1">
              <button v-if="row.status !== 'active'" type="button" class="action-button hover:bg-emerald-50 hover:text-emerald-600 dark:hover:bg-emerald-900/20 dark:hover:text-emerald-400" :disabled="busyStatus" :title="t('admin.videoProtocolProfiles.activate')" :data-testid="`video-profile-activate-${row.profile_key}-${row.version}`" @click="changeStatus(row, 'active')">
                <Icon name="play" size="sm" />
              </button>
              <button v-else type="button" class="action-button hover:bg-amber-50 hover:text-amber-600 dark:hover:bg-amber-900/20 dark:hover:text-amber-400" :disabled="busyStatus" :title="t('admin.videoProtocolProfiles.disable')" :data-testid="`video-profile-disable-${row.profile_key}-${row.version}`" @click="changeStatus(row, 'disabled')">
                <Icon name="ban" size="sm" />
              </button>
              <button type="button" class="action-button hover:bg-gray-100 hover:text-gray-700 dark:hover:bg-dark-600 dark:hover:text-gray-300" :title="t('admin.videoProtocolProfiles.edit')" :data-testid="`video-profile-edit-${row.profile_key}-${row.version}`" @click="openEditor(row, 'edit')">
                <Icon name="edit" size="sm" />
              </button>
              <button type="button" class="action-button hover:bg-blue-50 hover:text-blue-600 dark:hover:bg-blue-900/20 dark:hover:text-blue-400" :title="t('admin.videoProtocolProfiles.createFrom')" :data-testid="`video-profile-clone-${row.profile_key}-${row.version}`" @click="openEditor(row)">
                <Icon name="copy" size="sm" />
              </button>
              <button v-if="row.status !== 'active'" type="button" class="action-button hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-900/20 dark:hover:text-red-400" :disabled="busyStatus" :title="t('admin.videoProtocolProfiles.delete')" :data-testid="`video-profile-delete-${row.profile_key}-${row.version}`" @click="askDelete(row.profile_key, row.version)">
                <Icon name="trash" size="sm" />
              </button>
            </div>
          </template>

          <template #empty>
            <EmptyState :title="t('empty.noData')" :description="t('admin.videoProtocolProfiles.empty')" :action-text="t('admin.videoProtocolProfiles.create')" data-testid="video-profiles-empty" @action="openEditor()" />
          </template>
        </DataTable>
      </template>
    </TablePageLayout>

      <!-- 编辑器弹窗：保存总是生成新版本，在途任务继续使用创建时冻结的快照 -->
      <BaseDialog :show="!!editor" :title="editorTitle" width="full" @close="editor = null">
        <div v-if="editor" class="space-y-4" data-testid="video-profile-editor">
        <div class="flex flex-wrap items-start justify-between gap-2">
          <p class="text-xs text-gray-500 dark:text-gray-400">{{ editor.mode === 'edit' ? t('admin.videoProtocolProfiles.editHint') : t('admin.videoProtocolProfiles.editorHint') }}</p>
          <button type="button" class="btn btn-secondary btn-sm" data-testid="video-profile-ai-open-editor" @click="openAIDialog()">{{ t('admin.videoProtocolProfiles.ai.open') }}</button>
        </div>
        <div v-if="aiNotice" :class="['rounded-lg p-3 text-sm', aiNotice.valid ? 'bg-emerald-50 text-emerald-800 dark:bg-emerald-900/20 dark:text-emerald-200' : 'bg-amber-50 text-amber-800 dark:bg-amber-900/20 dark:text-amber-200']" data-testid="video-profile-ai-notice">
          <p class="font-medium">{{ aiNotice.valid ? t('admin.videoProtocolProfiles.ai.validDraft', { attempts: aiNotice.attempts }) : t('admin.videoProtocolProfiles.ai.invalidDraft', { error: aiNotice.validationError }) }}</p>
          <p v-if="aiNotice.notes" class="mt-1 whitespace-pre-wrap text-xs">{{ aiNotice.notes }}</p>
        </div>
        <div class="grid gap-3 sm:grid-cols-2">
          <div>
            <label class="input-label">{{ t('admin.videoProtocolProfiles.profileKey') }}</label>
            <input v-model="editor.key" class="input font-mono" maxlength="64" :disabled="editor.mode === 'edit'" data-testid="video-profile-key" />
            <p class="input-hint">{{ t('admin.videoProtocolProfiles.profileKeyHint') }}</p>
          </div>
          <div>
            <label class="input-label">{{ t('admin.videoProtocolProfiles.profileName') }}</label>
            <input v-model="editor.name" class="input" maxlength="128" data-testid="video-profile-name" />
          </div>
        </div>
        <div>
          <div class="mb-1.5 flex items-center justify-between gap-2">
            <label class="input-label mb-0">{{ t('admin.videoProtocolProfiles.spec') }}</label>
            <button type="button" class="btn btn-secondary btn-sm" data-testid="video-profile-reset-starter" @click="editor.spec = formatJson(VIDEO_PROFILE_STARTER_SPEC)">{{ t('admin.videoProtocolProfiles.resetStarter') }}</button>
          </div>
          <TextArea v-model="editor.spec" :rows="22" class="font-mono text-xs" :hint="specHint" data-testid="video-profile-spec" />
        </div>

        <!-- 调试器：离线运行模板映射，不访问上游 -->
        <div class="space-y-3 border-t border-gray-200 pt-4 dark:border-dark-700" data-testid="video-profile-debugger">
          <div class="flex flex-wrap items-start justify-between gap-2">
            <div>
              <h4 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.videoProtocolProfiles.debugger') }}</h4>
              <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.videoProtocolProfiles.debuggerHint') }}</p>
            </div>
            <button type="button" class="btn btn-secondary btn-sm" data-testid="video-profile-debug-sample" @click="resetDebugSample()">{{ t('admin.videoProtocolProfiles.sampleFromSpec') }}</button>
          </div>
          <div>
            <label class="input-label">{{ t('admin.videoProtocolProfiles.debugModel') }}</label>
            <input v-model="debug.model" class="input font-mono" data-testid="video-profile-debug-model" />
          </div>
          <div class="grid gap-3 lg:grid-cols-3">
            <TextArea v-model="debug.request" :label="t('admin.videoProtocolProfiles.debugRequest')" :rows="12" class="font-mono text-xs" data-testid="video-profile-debug-request" />
            <TextArea v-model="debug.createResponse" :label="t('admin.videoProtocolProfiles.debugCreateResponse')" :rows="12" class="font-mono text-xs" data-testid="video-profile-debug-create" />
            <TextArea v-model="debug.pollResponse" :label="t('admin.videoProtocolProfiles.debugPollResponse')" :rows="12" class="font-mono text-xs" data-testid="video-profile-debug-poll" />
          </div>
          <div class="flex justify-end">
            <button type="button" class="btn btn-primary" :disabled="previewing" data-testid="video-profile-run-preview" @click="runPreview">{{ t('admin.videoProtocolProfiles.runPreview') }}</button>
          </div>
          <p v-if="previewError" class="text-sm text-red-600 dark:text-red-400" role="alert" data-testid="video-profile-preview-error">{{ previewError }}</p>
          <div v-if="preview" class="space-y-3 rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-800" data-testid="video-profile-preview-result">
            <h5 class="font-semibold text-gray-900 dark:text-white">{{ t('admin.videoProtocolProfiles.result') }}</h5>
            <p v-if="preview.request_error" class="text-red-600 dark:text-red-400" data-testid="video-profile-preview-request-error">{{ t('admin.videoProtocolProfiles.requestError') }}：{{ preview.request_error }}</p>
            <dl class="grid gap-x-4 gap-y-1 sm:grid-cols-[10rem_1fr]">
              <dt class="text-gray-500 dark:text-gray-400">{{ t('admin.videoProtocolProfiles.createPath') }}</dt>
              <dd class="break-all font-mono" data-testid="video-profile-preview-create-path">{{ preview.create_path || none }}</dd>
              <dt class="text-gray-500 dark:text-gray-400">{{ t('admin.videoProtocolProfiles.pollPath') }}</dt>
              <dd class="break-all font-mono" data-testid="video-profile-preview-poll-path">{{ preview.poll_path || none }}</dd>
              <dt class="text-gray-500 dark:text-gray-400">{{ t('admin.videoProtocolProfiles.contentPath') }}</dt>
              <dd class="break-all font-mono" data-testid="video-profile-preview-content-path">{{ preview.content_path || none }}</dd>
            </dl>
            <div v-if="preview.create_body !== undefined">
              <h6 class="text-xs font-semibold text-gray-700 dark:text-gray-300">{{ t('admin.videoProtocolProfiles.createBody') }}</h6>
              <pre class="mt-1 max-h-72 overflow-auto rounded bg-white p-2 text-xs dark:bg-dark-900" data-testid="video-profile-preview-create-body">{{ formatJson(preview.create_body) }}</pre>
            </div>
            <div>
              <h6 class="text-xs font-semibold text-gray-700 dark:text-gray-300">{{ t('admin.videoProtocolProfiles.billing') }}</h6>
              <dl class="mt-1 grid gap-x-4 gap-y-1 sm:grid-cols-[10rem_1fr]" data-testid="video-profile-preview-billing">
                <template v-for="field in billingFields" :key="field">
                  <dt class="text-gray-500 dark:text-gray-400">{{ t(`admin.videoProtocolProfiles.billingFields.${field}`) }}</dt>
                  <dd class="font-mono">{{ displayValue(preview.billing?.[field]) }}</dd>
                </template>
              </dl>
            </div>
            <div v-for="section in observedSections" :key="section.key" :data-testid="`video-profile-preview-${section.key}`">
              <h6 class="text-xs font-semibold text-gray-700 dark:text-gray-300">{{ t(section.label) }}</h6>
              <dl class="mt-1 grid gap-x-4 gap-y-1 sm:grid-cols-[10rem_1fr]">
                <template v-for="field in observedFields" :key="field">
                  <dt class="text-gray-500 dark:text-gray-400">{{ t(`admin.videoProtocolProfiles.observedFields.${field}`) }}</dt>
                  <dd :class="['break-all font-mono', field === 'error' && section.value[field] ? 'text-red-600 dark:text-red-400' : '']">{{ displayValue(section.value[field]) }}</dd>
                </template>
              </dl>
            </div>
          </div>
        </div>
        </div>
        <template #footer>
          <div v-if="editor" class="flex flex-wrap justify-end gap-2">
            <button type="button" class="btn btn-secondary" :disabled="saving" data-testid="video-profile-cancel" @click="editor = null">{{ t('admin.videoProtocolProfiles.cancel') }}</button>
            <button type="button" class="btn btn-secondary" :disabled="saving" data-testid="video-profile-save-draft" @click="saveProfile(false)">{{ t('admin.videoProtocolProfiles.saveDraft') }}</button>
            <button type="button" class="btn btn-primary" :disabled="saving" data-testid="video-profile-save-active" @click="saveProfile(true)">{{ editor.mode === 'edit' && editor.sourceActive ? t('admin.videoProtocolProfiles.editSaveActive') : t('admin.videoProtocolProfiles.saveActive') }}</button>
          </div>
        </template>
      </BaseDialog>

      <!-- 删除确认：启用中或仍被账号绑定的模板由服务端拒绝并给出原因 -->
      <ConfirmDialog
        :show="!!pendingDelete"
        :title="t('admin.videoProtocolProfiles.deleteTitle')"
        :message="pendingDelete ? (pendingDelete.version ? t('admin.videoProtocolProfiles.deleteVersionConfirm', { key: pendingDelete.key, version: pendingDelete.version }) : t('admin.videoProtocolProfiles.deleteTemplateConfirm', { key: pendingDelete.key })) : ''"
        :confirm-text="t('admin.videoProtocolProfiles.delete')"
        :loading="busyStatus"
        danger
        @confirm="confirmDelete"
        @cancel="pendingDelete = null"
      />

      <!-- AI 生成：把上游接口文档交给系统设置中配置的模型，生成结果只回填编辑器，不会自动保存 -->
      <BaseDialog :show="aiDialog.show" :title="t('admin.videoProtocolProfiles.ai.title')" width="wide" :z-index="60" @close="aiDialog.show = false">
        <div class="space-y-3" data-testid="video-profile-ai-dialog">
          <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('admin.videoProtocolProfiles.ai.hint') }}</p>
          <TextArea v-model="aiDialog.document" :label="t('admin.videoProtocolProfiles.ai.document')" :rows="14" class="font-mono text-xs" :placeholder="t('admin.videoProtocolProfiles.ai.documentPlaceholder')" data-testid="video-profile-ai-document" />
          <TextArea v-model="aiDialog.instructions" :label="t('admin.videoProtocolProfiles.ai.instructions')" :rows="3" :placeholder="t('admin.videoProtocolProfiles.ai.instructionsPlaceholder')" data-testid="video-profile-ai-instructions" />
          <label v-if="editor" class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
            <input v-model="aiDialog.useCurrent" type="checkbox" class="h-4 w-4" data-testid="video-profile-ai-use-current" />
            {{ t('admin.videoProtocolProfiles.ai.useCurrent') }}
          </label>
          <p v-if="aiDialog.error" class="text-sm text-red-600 dark:text-red-400" role="alert" data-testid="video-profile-ai-error">{{ aiDialog.error }}</p>
        </div>
        <template #footer>
          <div class="flex justify-end gap-2">
            <button type="button" class="btn btn-secondary" :disabled="aiDialog.loading" @click="aiDialog.show = false">{{ t('admin.videoProtocolProfiles.cancel') }}</button>
            <button type="button" class="btn btn-primary" :disabled="aiDialog.loading" data-testid="video-profile-ai-generate" @click="generateWithAI">
              {{ aiDialog.loading ? t('admin.videoProtocolProfiles.ai.generating') : t('admin.videoProtocolProfiles.ai.generate') }}
            </button>
          </div>
        </template>
      </BaseDialog>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Select from '@/components/common/Select.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import Icon from '@/components/icons/Icon.vue'
import type { Column } from '@/components/common/types'
import TextArea from '@/components/common/TextArea.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import { adminAPI } from '@/api/admin'
import type { VideoProtocolProfile, VideoProtocolProfileObserved, VideoProtocolProfilePreview, VideoProtocolProfileStatus } from '@/api/admin/videoProtocolProfiles'
import { useAppStore } from '@/stores'
import { formatDateTime } from '@/utils/format'
import { VIDEO_PROFILE_KEY_PATTERN } from '@/components/account/videoAccountConfig'
import {
  VIDEO_PROFILE_SAMPLE_CREATE_RESPONSE, VIDEO_PROFILE_SAMPLE_POLL_RESPONSE, VIDEO_PROFILE_SAMPLE_REQUEST, VIDEO_PROFILE_STARTER_SPEC,
  formatJson, groupVideoProtocolProfiles, parseJsonObject, parseOptionalJson, videoProfileSampleRequest
} from './videoProtocolProfiles'

const { t } = useI18n()
const appStore = useAppStore()

const profiles = ref<VideoProtocolProfile[]>([])
const loading = ref(false)
const busyStatus = ref(false)
const saving = ref(false)
const previewing = ref(false)
const groups = computed(() => groupVideoProtocolProfiles(profiles.value))
// 列表筛选：按键名或名称搜索，按状态过滤。
const searchQuery = ref('')
const statusFilter = ref<string | number | boolean | null>('')
const statusFilterOptions = computed(() => [
  { value: '', label: t('admin.videoProtocolProfiles.allStatuses') },
  ...(['active', 'draft', 'disabled'] as const).map(status => ({ value: status, label: t(`admin.videoProtocolProfiles.statuses.${status}`) })),
])
const columns = computed<Column[]>(() => [
  { key: 'profile_key', label: t('admin.videoProtocolProfiles.profileKey') },
  { key: 'version', label: t('admin.videoProtocolProfiles.version') },
  { key: 'name', label: t('admin.videoProtocolProfiles.name') },
  { key: 'status', label: t('admin.videoProtocolProfiles.status') },
  { key: 'created_at', label: t('admin.videoProtocolProfiles.createdAt') },
  { key: 'spec_hash', label: t('admin.videoProtocolProfiles.specHash') },
  { key: 'actions', label: t('admin.videoProtocolProfiles.actions') },
])
// 分组展开为每版本一行；firstOfKey 标记每个键名的首行，用于放置整模板删除按钮。
const rows = computed(() => {
  const search = searchQuery.value.trim().toLowerCase()
  return groups.value.flatMap(group => {
    const versions = group.versions.filter(profile => (!statusFilter.value || profile.status === statusFilter.value)
      && (!search || profile.profile_key.toLowerCase().includes(search) || profile.name.toLowerCase().includes(search)))
    return versions.map((profile, index) => ({ ...profile, firstOfKey: index === 0, keyActiveVersion: group.active?.version ?? 0 }))
  })
})

// 编辑器状态；null 表示未打开。mode=edit 时键名锁定，保存为同键名的新版本；源版本启用时新版本自动启用。
interface EditorState { key: string; name: string; spec: string; mode: 'create' | 'edit'; sourceVersion?: number; sourceActive?: boolean }
const editor = ref<EditorState | null>(null)
const editorTitle = computed(() => editor.value?.mode === 'edit'
  ? t('admin.videoProtocolProfiles.editTitle', { key: editor.value.key, version: editor.value.sourceVersion })
  : t('admin.videoProtocolProfiles.editorTitle'))
// 待确认的删除：version 为空表示删除整个模板。
const pendingDelete = ref<{ key: string; version?: number } | null>(null)
// 最近一次 AI 生成的结果说明，展示在编辑器顶部。
const aiNotice = ref<{ valid: boolean; validationError?: string; notes: string; attempts: number } | null>(null)
const aiDialog = reactive({ show: false, document: '', instructions: '', useCurrent: false, loading: false, error: '' })
const debug = reactive({
  model: '',
  request: formatJson(VIDEO_PROFILE_SAMPLE_REQUEST),
  createResponse: formatJson(VIDEO_PROFILE_SAMPLE_CREATE_RESPONSE),
  pollResponse: formatJson(VIDEO_PROFILE_SAMPLE_POLL_RESPONSE),
})
const preview = ref<VideoProtocolProfilePreview | null>(null)
const previewError = ref('')
const none = computed(() => t('admin.videoProtocolProfiles.none'))
// 占位符作为参数传入，避免被 i18n 当作插值。
const specHint = computed(() => t('admin.videoProtocolProfiles.specHint', { taskId: '{task_id}', model: '{model}', ref: '{ref.name}' }))

const billingFields = ['model', 'resolution', 'duration_seconds', 'size', 'has_reference_video', 'reference_image_count', 'tokens'] as const
const observedFields = ['task_id', 'task_refs', 'status', 'upstream_status', 'video_url', 'seconds', 'tokens', 'resolution', 'matches_task', 'error'] as const
const observedSections = computed(() => {
  const sections: { key: string; label: string; value: VideoProtocolProfileObserved }[] = []
  if (preview.value?.create) sections.push({ key: 'create', label: 'admin.videoProtocolProfiles.createParsed', value: preview.value.create })
  if (preview.value?.poll) sections.push({ key: 'poll', label: 'admin.videoProtocolProfiles.pollParsed', value: preview.value.poll })
  return sections
})

function errorMessage(error: unknown, fallback: string): string {
  const message = typeof error === 'object' && error !== null && 'message' in error ? String((error as { message?: unknown }).message || '') : ''
  return message || fallback
}

// 调试结果统一格式化：布尔值本地化、对象转 JSON、空值显示占位。
function displayValue(value: unknown): string {
  if (value === undefined || value === null || value === '') return none.value
  if (typeof value === 'boolean') return t(value ? 'admin.videoProtocolProfiles.yes' : 'admin.videoProtocolProfiles.no')
  if (typeof value === 'object') return Object.keys(value as object).length ? JSON.stringify(value) : none.value
  return String(value)
}

async function loadProfiles() {
  loading.value = true
  try {
    profiles.value = await adminAPI.videoProtocolProfiles.list()
  } catch (error) {
    appStore.showError(errorMessage(error, t('admin.videoProtocolProfiles.loadFailed')))
  } finally {
    loading.value = false
  }
}

async function changeStatus(profile: VideoProtocolProfile, status: VideoProtocolProfileStatus) {
  busyStatus.value = true
  try {
    await adminAPI.videoProtocolProfiles.setStatus(profile.profile_key, profile.version, status)
    appStore.showSuccess(t('admin.videoProtocolProfiles.statusUpdated'))
    await loadProfiles()
  } catch (error) {
    appStore.showError(errorMessage(error, t('admin.videoProtocolProfiles.statusFailed')))
  } finally {
    busyStatus.value = false
  }
}

// 新建时使用示例模板；“编辑”锁定键名并沿用该版本内容，“基于此版本新建”复制内容但允许改键名。
function openEditor(base?: VideoProtocolProfile, mode: 'create' | 'edit' = 'create') {
  editor.value = base
    ? { key: base.profile_key, name: base.name, spec: formatJson(base.spec), mode, sourceVersion: base.version, sourceActive: base.status === 'active' }
    : { key: '', name: '', spec: formatJson(VIDEO_PROFILE_STARTER_SPEC), mode: 'create' }
  preview.value = null
  previewError.value = ''
  aiNotice.value = null
  resetDebugSample()
}

// 按当前编辑器中的模板生成调试样例；模板 JSON 暂时无法解析时保留原样例。
function resetDebugSample() {
  const spec = editor.value ? parseJsonObject(editor.value.spec) : null
  if (spec) debug.request = formatJson(videoProfileSampleRequest(spec))
}

function askDelete(key: string, version?: number) {
  pendingDelete.value = version ? { key, version } : { key }
}

// 删除后刷新列表；正在编辑被删除的模板时一并关闭编辑器。
async function confirmDelete() {
  const target = pendingDelete.value
  if (!target) return
  busyStatus.value = true
  try {
    if (target.version) await adminAPI.videoProtocolProfiles.removeVersion(target.key, target.version)
    else await adminAPI.videoProtocolProfiles.removeProfile(target.key)
    appStore.showSuccess(t('admin.videoProtocolProfiles.deleted'))
    if (editor.value?.mode === 'edit' && editor.value.key === target.key && (!target.version || editor.value.sourceVersion === target.version)) editor.value = null
    pendingDelete.value = null
    await loadProfiles()
  } catch (error) {
    appStore.showError(errorMessage(error, t('admin.videoProtocolProfiles.deleteFailed')))
  } finally {
    busyStatus.value = false
  }
}

function openAIDialog() {
  aiDialog.error = ''
  aiDialog.useCurrent = !!editor.value
  aiDialog.show = true
}

// 生成结果只回填编辑器：编辑模式保留键名；新建时空白键名与名称才使用 AI 建议，避免覆盖管理员输入。
async function generateWithAI() {
  aiDialog.error = ''
  if (!aiDialog.document.trim()) {
    aiDialog.error = t('admin.videoProtocolProfiles.ai.documentRequired')
    return
  }
  let baseSpec: Record<string, unknown> | undefined
  if (aiDialog.useCurrent && editor.value) {
    baseSpec = parseJsonObject(editor.value.spec) ?? undefined
  }
  aiDialog.loading = true
  try {
    const result = await adminAPI.videoProtocolProfiles.generateWithAI({
      document: aiDialog.document,
      ...(aiDialog.instructions.trim() ? { instructions: aiDialog.instructions.trim() } : {}),
      ...(baseSpec ? { base_spec: baseSpec } : {}),
    })
    if (!editor.value) openEditor()
    const current = editor.value!
    if (current.mode !== 'edit' && !current.key.trim() && result.profile_key) current.key = result.profile_key
    if (!current.name.trim() && result.name) current.name = result.name
    current.spec = formatJson(result.spec)
    resetDebugSample()
    aiNotice.value = { valid: result.valid, validationError: result.validation_error, notes: result.notes, attempts: result.attempts }
    preview.value = null
    aiDialog.show = false
  } catch (error) {
    aiDialog.error = errorMessage(error, t('admin.videoProtocolProfiles.ai.failed'))
  } finally {
    aiDialog.loading = false
  }
}

function editorSpec() {
  const spec = editor.value ? parseJsonObject(editor.value.spec) : null
  if (!spec) appStore.showError(t('admin.videoProtocolProfiles.invalidJson', { field: t('admin.videoProtocolProfiles.spec') }))
  return spec
}

async function saveProfile(activate: boolean) {
  if (!editor.value) return
  const key = editor.value.key.trim()
  if (!VIDEO_PROFILE_KEY_PATTERN.test(key)) {
    appStore.showError(t('admin.videoProtocolProfiles.profileKeyInvalid'))
    return
  }
  if (!editor.value.name.trim()) {
    appStore.showError(t('admin.videoProtocolProfiles.profileNameRequired'))
    return
  }
  const spec = editorSpec()
  if (!spec) return
  saving.value = true
  try {
    const saved = await adminAPI.videoProtocolProfiles.save({ profile_key: key, name: editor.value.name.trim(), spec, activate })
    appStore.showSuccess(t('admin.videoProtocolProfiles.saved', { version: saved.version }))
    // 编辑后的新版本已成为当前内容，继续编辑时以它为源版本。
    if (editor.value?.mode === 'edit') {
      editor.value.sourceVersion = saved.version
      editor.value.sourceActive = saved.status === 'active'
    }
    await loadProfiles()
  } catch (error) {
    appStore.showError(errorMessage(error, t('admin.videoProtocolProfiles.saveFailed')))
  } finally {
    saving.value = false
  }
}

async function runPreview() {
  previewError.value = ''
  const spec = editorSpec()
  if (!spec) return
  const request = parseJsonObject(debug.request)
  if (!request) {
    previewError.value = t('admin.videoProtocolProfiles.invalidJson', { field: t('admin.videoProtocolProfiles.debugRequest') })
    return
  }
  const createResponse = parseOptionalJson(debug.createResponse)
  const pollResponse = parseOptionalJson(debug.pollResponse)
  if (createResponse === null || pollResponse === null) {
    previewError.value = t('admin.videoProtocolProfiles.invalidJson', {
      field: t(createResponse === null ? 'admin.videoProtocolProfiles.debugCreateResponse' : 'admin.videoProtocolProfiles.debugPollResponse'),
    })
    return
  }
  previewing.value = true
  try {
    preview.value = await adminAPI.videoProtocolProfiles.preview({
      spec, request,
      ...(debug.model.trim() ? { model: debug.model.trim() } : {}),
      ...(createResponse !== undefined ? { create_response: createResponse } : {}),
      ...(pollResponse !== undefined ? { poll_response: pollResponse } : {}),
    })
  } catch (error) {
    preview.value = null
    previewError.value = errorMessage(error, t('admin.videoProtocolProfiles.previewFailed'))
  } finally {
    previewing.value = false
  }
}

onMounted(loadProfiles)
</script>

<style scoped>
/* 行内操作沿用后台列表的图标按钮样式，悬停颜色由各按钮单独指定。 */
.action-button { @apply flex flex-col items-center gap-0.5 rounded-lg p-1.5 text-gray-500 transition-colors disabled:cursor-not-allowed disabled:opacity-50; }
</style>
