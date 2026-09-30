<template>
  <AppLayout>
    <template #page-heading-actions>
      <button type="button" class="btn btn-secondary" :disabled="loading" @click="refresh">{{ t('common.refresh') }}</button>
    </template>
    <div class="min-w-0 space-y-5">
      <div class="rounded-xl border border-blue-100 bg-blue-50 p-4 text-sm leading-6 text-blue-800 dark:border-blue-900 dark:bg-blue-950/30 dark:text-blue-200">
        <p>{{ t('mediaTasks.observationHint') }}</p>
        <p>{{ t('mediaTasks.billingHint') }}</p>
      </div>
      <form class="hidden flex-wrap items-end gap-3 lg:flex" @submit.prevent="search">
        <div class="w-36"><label class="input-label" for="media-type">{{ t('mediaTasks.type') }}</label><Select id="media-type" v-model="filters.media_type" :options="typeOptions" @change="search" /></div>
        <div class="w-40"><label class="input-label" for="media-status">{{ t('mediaTasks.status') }}</label><Select id="media-status" v-model="filters.status" :options="statusOptions" @change="search" /></div>
        <div class="w-48"><label class="input-label" for="media-source">{{ t('mediaTasks.source') }}</label><Select id="media-source" v-model="filters.source" :options="sourceOptions" @change="search" /></div>
        <div class="min-w-40 flex-1"><label class="input-label" for="media-model">{{ t('mediaTasks.model') }}</label><Select id="media-model" v-model="filters.model" :options="modelOptions" searchable :disabled="modelsLoading" @change="search" /></div>
        <div v-if="admin" class="w-64"><label class="input-label" for="media-user">{{ t('mediaTasks.user') }}</label><MediaTaskUserFilter id="media-user" v-model="selectedUser" @change="changeUser" /></div>
        <button type="submit" class="btn btn-secondary">{{ t('common.search') }}</button>
      </form>
      <!-- 移动端沿用账号管理的紧凑搜索和折叠筛选，共用同一份筛选状态。 -->
      <form class="space-y-3 lg:hidden" data-testid="mobile-task-filters" @submit.prevent="search">
        <div class="flex min-w-0 items-end gap-2">
          <div class="min-w-0 flex-1">
            <label class="input-label" for="media-model-mobile">{{ t('mediaTasks.model') }}</label>
            <Select id="media-model-mobile" v-model="filters.model" :options="modelOptions" searchable :disabled="modelsLoading" @change="search" />
          </div>
          <button type="submit" class="btn btn-secondary h-9 w-9 shrink-0 p-0" :aria-label="t('common.search')" :title="t('common.search')"><Icon name="search" size="sm" /></button>
          <button
            type="button"
            class="btn btn-secondary relative h-9 w-9 shrink-0 p-0"
            :class="activeFilterCount ? 'border-primary-400 text-primary-700 dark:border-primary-500 dark:text-primary-300' : ''"
            :aria-label="t('common.filter')"
            :title="t('common.filter')"
            :aria-expanded="showMobileFilters"
            aria-controls="media-mobile-filter-panel"
            data-testid="mobile-task-filters-toggle"
            @click="showMobileFilters = !showMobileFilters"
          >
            <Icon name="filter" size="sm" />
            <span v-if="activeFilterCount" class="pointer-events-none absolute -right-1 -top-1 inline-flex h-5 min-w-5 items-center justify-center rounded-full bg-primary-100 px-1 text-xs font-semibold text-primary-700 dark:bg-primary-900 dark:text-primary-300">{{ activeFilterCount }}</span>
          </button>
        </div>
        <div v-show="showMobileFilters" id="media-mobile-filter-panel" class="card p-4" @keydown.esc.stop="showMobileFilters = false">
          <div class="mb-3 flex items-center justify-between gap-3">
            <span class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('common.filter') }}</span>
            <button v-if="activeFilterCount" type="button" class="text-xs font-medium text-primary-600 dark:text-primary-400" data-testid="mobile-task-filters-reset" @click="resetFilters">{{ t('common.reset') }}</button>
          </div>
          <div class="grid min-w-0 grid-cols-1 gap-3 sm:grid-cols-2">
            <div class="min-w-0"><label class="input-label" for="media-type-mobile">{{ t('mediaTasks.type') }}</label><Select id="media-type-mobile" v-model="filters.media_type" :options="typeOptions" @change="search" /></div>
            <div class="min-w-0"><label class="input-label" for="media-status-mobile">{{ t('mediaTasks.status') }}</label><Select id="media-status-mobile" v-model="filters.status" :options="statusOptions" @change="search" /></div>
            <div class="min-w-0"><label class="input-label" for="media-source-mobile">{{ t('mediaTasks.source') }}</label><Select id="media-source-mobile" v-model="filters.source" :options="sourceOptions" @change="search" /></div>
            <div v-if="admin" class="min-w-0"><label class="input-label" for="media-user-mobile">{{ t('mediaTasks.user') }}</label><MediaTaskUserFilter id="media-user-mobile" v-model="selectedUser" @change="changeUser" /></div>
          </div>
          <button type="submit" class="btn btn-secondary mt-4 w-full">{{ t('common.search') }}</button>
        </div>
      </form>
      <p v-if="modelsError" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ modelsError }}</p>
      <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
      <div class="media-task-results card overflow-hidden">
        <div v-if="loading" class="card p-12 text-center text-gray-500 lg:rounded-none lg:border-0" role="status">{{ t('common.loading') }}</div>
        <div v-else-if="!items.length" class="card p-12 text-center text-gray-500 lg:rounded-none lg:border-0">{{ t('mediaTasks.empty') }}</div>
        <template v-else>
        <!-- 与账号管理使用相同的 lg 断点，手机端每条记录独立成卡，操作按钮整行展示。 -->
        <div class="space-y-3 lg:hidden" data-testid="mobile-task-list">
          <article v-for="task in items" :key="task.id" class="card min-w-0 p-4" data-testid="mobile-task-card">
            <dl class="space-y-3 text-sm">
              <div class="flex min-w-0 items-start justify-between gap-4">
                <dt class="shrink-0 text-xs text-gray-500 dark:text-gray-400">{{ t('mediaTasks.task') }}</dt>
                <dd class="min-w-0 text-right">
                  <button type="button" class="max-w-full break-all text-right font-mono text-xs text-primary-600 hover:underline dark:text-primary-400" @click="openDetails(task.id)">{{ task.task_id }}</button>
                  <p class="mt-1 break-words text-xs text-gray-500">{{ label('types', task.media_type) }} · {{ label('sources', task.source) }}</p>
                </dd>
              </div>
              <div v-if="admin" class="flex min-w-0 items-start justify-between gap-4" data-testid="task-user-card">
                <dt class="shrink-0 text-xs text-gray-500 dark:text-gray-400">{{ t('mediaTasks.user') }}</dt>
                <dd class="min-w-0 text-right">
                  <span class="break-all font-medium text-gray-900 dark:text-white" :title="userTitle(task)">{{ userDisplayName(task) || '—' }}</span>
                  <div class="mt-1 flex flex-wrap items-center justify-end gap-1 text-xs">
                    <span v-if="task.user?.deleted_at" class="rounded bg-rose-100 px-1 py-px text-rose-600 dark:bg-rose-500/20 dark:text-rose-400">{{ t('admin.usage.userDeletedBadge') }}</span>
                    <span class="text-gray-500 dark:text-gray-400">#{{ task.user_id }}</span>
                  </div>
                </dd>
              </div>
              <div class="flex min-w-0 items-start justify-between gap-4">
                <dt class="shrink-0 text-xs text-gray-500 dark:text-gray-400">{{ t('mediaTasks.model') }}</dt>
                <dd class="min-w-0 text-right"><p class="break-all text-gray-900 dark:text-gray-100">{{ task.model }}</p><p class="mt-1 break-all text-xs text-gray-500">{{ task.group_name || task.platform || '—' }}</p></dd>
              </div>
              <div class="flex min-w-0 items-start justify-between gap-4">
                <dt class="shrink-0 text-xs text-gray-500 dark:text-gray-400">{{ t('mediaTasks.status') }}</dt>
                <dd class="min-w-0 text-right"><span class="inline-block rounded-full px-2 py-1 text-xs font-medium" :class="statusClass(task.status)">{{ label('statuses', task.status) }}</span></dd>
              </div>
              <div class="flex min-w-0 items-start justify-between gap-4">
                <dt class="max-w-[60%] text-xs text-gray-500 dark:text-gray-400">{{ t('mediaTasks.cost') }}</dt>
                <dd class="min-w-0 break-all text-right tabular-nums text-gray-900 dark:text-gray-100">{{ formatCost(task.actual_cost) }}</dd>
              </div>
              <div class="flex min-w-0 items-start justify-between gap-4">
                <dt class="shrink-0 text-xs text-gray-500 dark:text-gray-400">{{ t('mediaTasks.updatedAt') }}</dt>
                <dd class="min-w-0 break-words text-right text-xs text-gray-500">{{ formatTime(task.updated_at) }}</dd>
              </div>
            </dl>
            <div class="mt-4 flex flex-wrap gap-2 border-t border-gray-100 pt-3 dark:border-dark-700"><button v-if="task.status === 'completed'" type="button" class="btn btn-secondary min-h-10 w-full" @click="openPreview(task)">{{ t('mediaTasks.preview.open') }}</button><button type="button" class="btn btn-secondary min-h-10 w-full" @click="openDetails(task.id)">{{ t('mediaTasks.details') }}</button></div>
          </article>
        </div>
        <div class="hidden overflow-x-auto lg:block" data-testid="desktop-task-list">
          <table class="w-full text-left text-sm">
            <thead class="bg-gray-50 text-xs text-gray-500 dark:bg-dark-800"><tr>
              <th class="px-5 py-3">{{ t('mediaTasks.task') }}</th><th v-if="admin" class="px-4 py-3">{{ t('mediaTasks.user') }}</th>
              <th class="px-4 py-3">{{ t('mediaTasks.model') }}</th><th class="px-4 py-3">{{ t('mediaTasks.status') }}</th>
              <th class="px-4 py-3">{{ t('mediaTasks.cost') }}</th><th class="px-4 py-3">{{ t('mediaTasks.updatedAt') }}</th><th class="px-4 py-3">{{ t('common.actions') }}</th>
            </tr></thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700"><tr v-for="task in items" :key="task.id" class="hover:bg-gray-50 dark:hover:bg-dark-800/50">
              <td class="max-w-xs px-5 py-4"><button type="button" class="block max-w-64 truncate text-left font-mono text-xs text-primary-600 hover:underline dark:text-primary-400" :title="task.task_id" @click="openDetails(task.id)">{{ task.task_id }}</button><p class="mt-1 text-xs text-gray-500">{{ label('types', task.media_type) }} · {{ label('sources', task.source) }}</p></td>
              <td v-if="admin" class="px-4 py-4">
                <!-- 与使用日志保持一致：名称省略显示，完整身份放入悬停提示。 -->
                <div class="flex w-36 items-center gap-1" data-testid="task-user-cell">
                  <span class="min-w-0 truncate font-medium text-gray-900 dark:text-white" :title="userTitle(task)">{{ userDisplayName(task) || '—' }}</span>
                  <span v-if="task.user?.deleted_at" class="inline-flex shrink-0 items-center rounded bg-rose-100 px-1 py-px text-[10px] font-medium leading-tight text-rose-600 ring-1 ring-inset ring-rose-200 dark:bg-rose-500/20 dark:text-rose-400 dark:ring-rose-500/30">{{ t('admin.usage.userDeletedBadge') }}</span>
                  <span class="shrink-0 text-gray-500 dark:text-gray-400">#{{ task.user_id }}</span>
                </div>
              </td>
              <td class="max-w-xs px-4 py-4"><p class="break-words">{{ task.model }}</p><p class="mt-1 text-xs text-gray-500">{{ task.group_name || task.platform || '—' }}</p></td>
              <td class="whitespace-nowrap px-4 py-4"><span class="rounded-full px-2 py-1 text-xs font-medium" :class="statusClass(task.status)">{{ label('statuses', task.status) }}</span></td>
              <td class="whitespace-nowrap px-4 py-4 tabular-nums">{{ formatCost(task.actual_cost) }}</td>
              <td class="whitespace-nowrap px-4 py-4 text-xs text-gray-500">{{ formatTime(task.updated_at) }}</td>
              <td class="px-4 py-4"><div class="flex gap-2"><button v-if="task.status === 'completed'" type="button" class="btn btn-secondary btn-sm whitespace-nowrap" @click="openPreview(task)">{{ t('mediaTasks.preview.open') }}</button><button type="button" class="btn btn-secondary btn-sm whitespace-nowrap" @click="openDetails(task.id)">{{ t('mediaTasks.details') }}</button></div></td>
            </tr></tbody>
          </table>
        </div>
        </template>
        <Pagination v-if="total > 0" class="media-task-pagination" :page="page" :page-size="pageSize" :total="total" @update:page="changePage" @update:page-size="changePageSize" />
      </div>
    </div>
    <MediaTaskPreviewDialog :show="previewOpen" :task="previewTask" :preview="preview" :loading="previewLoading" :error="previewError" :admin="admin" @close="closePreview" @retry="retryPreview" />
    <BaseDialog :show="detailsOpen" :title="t('mediaTasks.details')" width="wide" @close="closeDetails">
      <p v-if="detailsLoading" role="status" class="py-8 text-center text-gray-500">{{ t('common.loading') }}</p>
      <p v-else-if="detailsError" role="alert" class="text-sm text-red-600">{{ detailsError }}</p>
      <template v-else-if="detail">
        <dl class="grid grid-cols-1 gap-4 text-sm sm:grid-cols-2"><div v-for="field in detailFields" :key="field.key" class="min-w-0"><dt class="text-xs text-gray-500">{{ t(`mediaTasks.${field.key}`) }}</dt><dd class="mt-1 break-all text-gray-900 dark:text-gray-100">{{ field.value }}</dd></div></dl>
        <div v-if="detail.error_message" class="mt-5 min-w-0"><p class="text-xs text-gray-500">{{ t('mediaTasks.error') }}</p><p class="mt-1 whitespace-pre-wrap break-words text-sm text-red-600 [overflow-wrap:anywhere] dark:text-red-400">{{ detail.error_message }}</p></div>
        <p class="mt-5 border-t border-gray-100 pt-4 text-xs leading-5 text-gray-500 dark:border-dark-700">{{ t('mediaTasks.billingHint') }}</p>
      </template>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import Pagination from '@/components/common/Pagination.vue'
import Icon from '@/components/icons/Icon.vue'
import MediaTaskUserFilter from '@/components/media/MediaTaskUserFilter.vue'
import MediaTaskPreviewDialog from '@/components/media/MediaTaskPreviewDialog.vue'
import type { SimpleUser } from '@/api/admin/usage'
import { mediaTasksAPI, mediaTaskTypes, mediaTaskStatuses, mediaTaskSources, type MediaTask, type MediaTaskPreview } from '@/api/mediaTasks'
import { extractApiErrorMessage } from '@/utils/apiError'

const props = withDefaults(defineProps<{ admin?: boolean }>(), { admin: false })
const { t, te } = useI18n()
const filters = reactive({ media_type: '', status: '', source: '', model: '', user_id: '' })
const selectedUser = ref<SimpleUser | null>(null)
const models = ref<string[]>([])
const modelsLoading = ref(false)
const modelsError = ref('')
const modelOptions = computed(() => [{ value: '', label: t('mediaTasks.all') }, ...Array.from(new Set([...models.value, ...(filters.model ? [filters.model] : [])])).map(model => ({ value: model, label: model }))])
const previewOpen = ref(false)
const previewTask = ref<MediaTask | null>(null)
const preview = ref<MediaTaskPreview | null>(null)
const previewLoading = ref(false)
const previewError = ref('')
let modelsRequest = 0
let previewRequest = 0
const showMobileFilters = ref(false)
const activeFilterCount = computed(() => [filters.media_type, filters.status, filters.source, ...(props.admin ? [filters.user_id] : [])].filter(value => String(value).trim() !== '').length)
const items = ref<MediaTask[]>([])
const page = ref(1)
const pageSize = ref(20)
const total = ref(0)
const loading = ref(false)
const error = ref('')
const detail = ref<MediaTask | null>(null)
const detailsOpen = ref(false)
const detailsLoading = ref(false)
const detailsError = ref('')
let listRequest = 0
let detailRequest = 0

function label(kind: string, value: string) {
  const key = `mediaTasks.${kind}.${value}`
  return te(key) ? t(key) : value || '—'
}
const options = (kind: string, values: readonly string[]) => [{ value: '', label: t('mediaTasks.all') }, ...values.map(value => ({ value, label: label(kind, value) }))]
const typeOptions = computed(() => options('types', mediaTaskTypes))
const statusOptions = computed(() => options('statuses', mediaTaskStatuses))
const sourceOptions = computed(() => options('sources', mediaTaskSources))
const formatTime = (value: string | null) => value ? new Date(value).toLocaleString() : '—'
// 沿用使用日志的用户名优先及邮箱缩略规则，保留 ID 以区分同名用户。
function userDisplayName(task: MediaTask): string {
  const username = task.user?.username?.trim() || ''
  if (username) return username
  const localPart = task.user?.email?.trim().split('@', 1)[0]?.trim() || ''
  const characters = Array.from(localPart)
  return characters.length ? `${characters[0]}***${characters[characters.length - 1]}` : ''
}
function userTitle(task: MediaTask): string {
  const username = task.user?.username?.trim() || ''
  const email = task.user?.email?.trim() || ''
  return username && email && username !== email ? `${username} (${email})` : username || email
}
function formatCost(value: MediaTask['actual_cost']) {
  // 未确认费用与明确零费用必须区分，完成状态不能替代账本结果。
  if (value === null || value === undefined || value === '') return t('mediaTasks.pendingCost')
  const amount = Number(value)
  return Number.isFinite(amount) ? `$${amount.toFixed(10).replace(/0+$/, '').replace(/\.$/, '.00')}` : t('mediaTasks.pendingCost')
}
function statusClass(status: string) {
  if (status === 'completed') return 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'
  if (status === 'failed') return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
  if (status === 'queued' || status === 'processing') return 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
  return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
}
const detailFields = computed(() => {
  const task = detail.value
  if (!task) return []
  return [
    { key: 'task', value: task.task_id }, { key: 'source', value: label('sources', task.source) },
    { key: 'type', value: label('types', task.media_type) }, { key: 'platform', value: task.platform },
    { key: 'model', value: task.model }, { key: 'status', value: label('statuses', task.status) },
    { key: 'upstreamStatus', value: task.upstream_status || '—' }, { key: 'cost', value: formatCost(task.actual_cost) },
    { key: 'billingMode', value: task.billing_mode || '—' }, { key: 'group', value: task.group_name || task.group_id || '—' },
    { key: 'apiKeyId', value: task.api_key_id }, { key: 'requestId', value: task.request_id || '—' },
    ...(props.admin ? [{ key: 'user', value: `${userTitle(task) || '—'} #${task.user_id}` }, { key: 'accountId', value: task.account_id ?? '—' }] : []),
    { key: 'httpStatus', value: task.http_status || '—' }, { key: 'createdAt', value: formatTime(task.created_at) },
    { key: 'updatedAt', value: formatTime(task.updated_at) }, { key: 'completedAt', value: formatTime(task.completed_at) },
    { key: 'expiresAt', value: formatTime(task.expires_at) },
  ]
})

async function load() {
  const current = ++listRequest
  loading.value = true
  error.value = ''
  try {
    const userID = Number(filters.user_id)
    const result = await mediaTasksAPI(props.admin).list({
      page: page.value, page_size: pageSize.value,
      media_type: filters.media_type || undefined, status: filters.status || undefined,
      source: filters.source || undefined, model: filters.model || undefined,
      model_exact: true,
      ...(props.admin && Number.isSafeInteger(userID) && userID > 0 ? { user_id: userID } : {}),
    })
    // 筛选、翻页及角色入口切换后，旧请求不得覆盖新的列表。
    if (current !== listRequest) return
    items.value = result.items
    total.value = result.total
    page.value = result.page
  } catch (err) {
    if (current !== listRequest) return
    items.value = []
    total.value = 0
    error.value = extractApiErrorMessage(err, t('mediaTasks.loadFailed'))
  } finally {
    if (current === listRequest) loading.value = false
  }
}
async function loadModels() {
  const request = ++modelsRequest
  modelsLoading.value = true
  modelsError.value = ''
  try {
    const result = await mediaTasksAPI(props.admin).models({
      media_type: filters.media_type || undefined, status: filters.status || undefined,
      source: filters.source || undefined,
      ...(props.admin && selectedUser.value ? { user_id: selectedUser.value.id } : {}),
    })
    if (request === modelsRequest) models.value = result
  } catch (err) {
    if (request === modelsRequest) { models.value = []; modelsError.value = extractApiErrorMessage(err, t('mediaTasks.modelsLoadFailed')) }
  } finally {
    if (request === modelsRequest) modelsLoading.value = false
  }
}
function refresh() { void load(); void loadModels() }
function search() { page.value = 1; refresh() }
function changeUser() {
  // 用户变更时清空上一个用户的模型候选，避免跨用户的晚到响应污染选择框。
  filters.user_id = selectedUser.value ? String(selectedUser.value.id) : ''
  filters.model = ''
  models.value = []
  search()
}
async function openPreview(task: MediaTask) {
  const request = ++previewRequest
  previewOpen.value = true
  previewTask.value = task
  preview.value = null
  previewError.value = ''
  previewLoading.value = true
  const api = mediaTasksAPI(props.admin)
  try {
    const [latest, result] = await Promise.all([api.get(task.id), api.preview(task.id)])
    if (request === previewRequest) { previewTask.value = latest; preview.value = result }
  } catch (err) {
    if (request === previewRequest) previewError.value = extractApiErrorMessage(err, t('mediaTasks.loadFailed'))
  } finally {
    if (request === previewRequest) previewLoading.value = false
  }
}
function closePreview() { ++previewRequest; previewOpen.value = false; previewTask.value = null; preview.value = null }
function retryPreview() { if (previewTask.value) void openPreview(previewTask.value) }
function resetFilters() {
  // 模型选择独立于折叠筛选，重置筛选时保留当前选中的模型。
  filters.media_type = ''; filters.status = ''; filters.source = ''; filters.user_id = ''; selectedUser.value = null
  search()
}
function changePage(value: number) { page.value = value; void load() }
function changePageSize(value: number) { pageSize.value = value; search() }
async function openDetails(id: number) {
  const current = ++detailRequest
  detailsOpen.value = true
  detailsLoading.value = true
  detailsError.value = ''
  detail.value = null
  try {
    const result = await mediaTasksAPI(props.admin).get(id)
    if (current === detailRequest) detail.value = result
  } catch (err) {
    if (current === detailRequest) detailsError.value = extractApiErrorMessage(err, t('mediaTasks.loadFailed'))
  } finally {
    if (current === detailRequest) detailsLoading.value = false
  }
}
function closeDetails() { ++detailRequest; detailsOpen.value = false; detail.value = null }
watch(() => props.admin, () => { closeDetails(); closePreview(); selectedUser.value = null; filters.user_id = ''; filters.model = ''; models.value = []; items.value = []; search() })
onMounted(refresh)
onBeforeUnmount(() => { ++listRequest; ++detailRequest; ++modelsRequest; ++previewRequest })
</script>

<style scoped>
/* 桌面保留现有表格外框，移动端只让独立卡片与分页承载边框。 */
@media (max-width: 1023px) {
  .media-task-results {
    @apply overflow-visible rounded-none border-0 bg-transparent dark:bg-transparent;
  }

  .media-task-pagination {
    @apply mt-3 rounded-surface border border-primary-900/10 dark:border-dark-600/80;
  }
}
</style>
