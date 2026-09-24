<template>
  <AppLayout>
    <p v-if="disabled" role="status" class="rounded-xl bg-amber-50 p-4 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-300">{{ t('tickets.disabled') }}</p>
    <button v-if="disabled" type="button" class="btn btn-secondary mt-3" :disabled="loading || closing" @click="refresh">{{ t('common.refresh') }}</button>
    <!-- 与账号管理共用响应式表格：手机为字段卡片，桌面为独立滚动表体。 -->
    <TablePageLayout v-else class="ticket-list">
      <template #filters>
        <div class="flex flex-col-reverse gap-3 lg:flex-row lg:items-start lg:justify-between">
          <form class="flex min-w-0 flex-1 items-center gap-2" @submit.prevent="search">
            <div class="min-w-0 flex-1">
              <label class="sr-only" for="filter-ticket-query">{{ t('tickets.search') }}</label>
              <input id="filter-ticket-query" v-model="filters.q" type="search" maxlength="200" class="input h-9 w-full" :placeholder="t('tickets.searchPlaceholder')" />
            </div>
            <button type="submit" class="btn btn-secondary h-9 shrink-0">{{ t('common.search') }}</button>
            <!-- 筛选面板按需展开，避免手机首屏被多个下拉框占满。 -->
            <details ref="filterPanel" class="relative shrink-0" @keydown.esc="closeFilters">
              <summary class="btn btn-secondary flex h-9 w-9 cursor-pointer list-none items-center justify-center p-0 [&::-webkit-details-marker]:hidden" :class="{ 'border-primary-400 text-primary-600': filters.type || filters.status || filters.priority }" :aria-label="t('common.filter')" :title="t('common.filter')">
                <Icon name="filter" size="sm" />
              </summary>
              <div class="absolute right-0 top-full z-40 mt-2 grid w-[min(26rem,calc(100vw-2rem))] grid-cols-2 gap-3 rounded-xl border border-gray-200 bg-white p-4 shadow-xl dark:border-dark-600 dark:bg-dark-900">
                <div class="col-span-2 flex items-center justify-between gap-2">
                  <span class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('common.filter') }}</span>
                  <button type="button" class="flex h-8 w-8 items-center justify-center rounded-lg text-gray-500 hover:bg-gray-100 dark:hover:bg-dark-800" :aria-label="t('common.close')" @click="closeFilters"><Icon name="x" size="sm" /></button>
                </div>
                <div class="min-w-0"><label class="input-label" for="filter-ticket-type">{{ t('tickets.type') }}</label><Select id="filter-ticket-type" v-model="filters.type" :options="typeOptions" @change="search" /></div>
                <div class="min-w-0"><label class="input-label" for="filter-ticket-status">{{ t('tickets.status') }}</label><Select id="filter-ticket-status" v-model="filters.status" :options="statusOptions" @change="search" /></div>
                <div class="col-span-2 min-w-0"><label class="input-label" for="filter-ticket-priority">{{ t('tickets.priority') }}</label><Select id="filter-ticket-priority" v-model="filters.priority" :options="priorityOptions" @change="search" /></div>
              </div>
            </details>
          </form>
          <div class="flex shrink-0 justify-end gap-2">
            <button type="button" class="btn btn-secondary h-9" :disabled="loading || closing" @click="refresh">{{ t('common.refresh') }}</button>
            <button v-if="!admin" type="button" class="btn btn-primary h-9" :disabled="!settings?.enabled" @click="showCreate = true">{{ t('tickets.create') }}</button>
          </div>
        </div>
        <p v-if="error" role="alert" class="mt-3 break-words text-sm text-red-600">{{ error }}</p>
      </template>
      <template #table>
        <DataTable :columns="columns" :data="items" :loading="loading" row-key="id" :expandable-actions="false" :sticky-first-column="false">
          <template #empty>
            <p class="font-medium text-gray-900 dark:text-white">{{ t('tickets.empty') }}</p>
            <p class="mt-2 text-sm text-gray-500">{{ t(admin ? 'tickets.emptyAdminHint' : 'tickets.emptyHint') }}</p>
          </template>
          <template #cell-title="{ row: ticket }">
            <RouterLink :to="`${base}/${ticket.id}`" :title="ticket.title" class="block max-w-full truncate font-medium text-primary-600 hover:underline dark:text-primary-400 lg:max-w-sm">{{ ticket.title }}</RouterLink>
            <span class="mt-1 block text-xs text-gray-400">#{{ ticket.id }}</span>
          </template>
          <template #cell-user="{ row: ticket }">
            <p class="truncate lg:max-w-56" :title="ticket.user_name">{{ ticket.user_name }}</p>
            <p class="break-all text-xs text-gray-500 lg:max-w-56 lg:truncate" :title="ticket.user_email">{{ ticket.user_email }}</p>
          </template>
          <template #cell-type="{ row: ticket }">{{ t(`tickets.types.${ticket.type}`) }}</template>
          <template #cell-priority="{ row: ticket }"><TicketBadge kind="priority" :value="ticket.priority" /></template>
          <template #cell-status="{ row: ticket }"><TicketBadge kind="status" :value="ticket.status" :closed-by-role="ticket.closed_by_role" /></template>
          <template #cell-updated_at="{ row: ticket }"><span class="text-xs text-gray-500">{{ new Date(ticket.updated_at).toLocaleString() }}</span></template>
          <template #cell-actions="{ row: ticket }">
            <div v-if="isOpen(ticket)" class="flex justify-end gap-2 lg:justify-start">
              <button type="button" class="btn btn-secondary min-h-9 flex-1 lg:flex-none" :disabled="closing" @click="requestClose(ticket, 'complete')">{{ t('tickets.complete') }}</button>
              <button type="button" class="btn btn-secondary min-h-9 flex-1 lg:flex-none" :disabled="closing" @click="requestClose(ticket, 'cancel')">{{ t('tickets.cancel') }}</button>
            </div>
            <span v-else class="text-gray-400">—</span>
          </template>
        </DataTable>
      </template>
      <template #pagination>
        <Pagination v-if="total > 0" :page="page" :page-size="pageSize" :total="total" @update:page="changePage" @update:page-size="changePageSize" />
      </template>
    </TablePageLayout>
    <CreateTicketDialog v-if="settings && !admin && !disabled" :show="showCreate" :settings="settings" @close="showCreate = false" @created="created" @disabled="markDisabled" />
    <ConfirmDialog :show="!!closeTarget" :title="t(closeTarget?.action === 'cancel' ? 'tickets.cancel' : 'tickets.complete')" :message="t(closeTarget?.action === 'cancel' ? 'tickets.cancelConfirm' : 'tickets.completeConfirm')" :danger="closeTarget?.action === 'cancel'" :loading="closing" @cancel="!closing && (closeTarget = null)" @confirm="closeTicket"><p class="break-words text-sm text-gray-500">{{ closeTarget?.ticket.title }}</p></ConfirmDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, watch } from 'vue'
import { RouterLink, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Icon from '@/components/icons/Icon.vue'
import type { Column } from '@/components/common/types'
import Select from '@/components/common/Select.vue'
import Pagination from '@/components/common/Pagination.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import TicketBadge from '@/components/tickets/TicketBadge.vue'
import CreateTicketDialog from '@/components/tickets/CreateTicketDialog.vue'
import { ticketAPI, ticketTypes, ticketPriorities, ticketStatuses, type Ticket, type TicketSettings } from '@/api/tickets'
import { extractApiErrorCode, extractI18nErrorMessage } from '@/utils/apiError'
import { useAppStore } from '@/stores/app'

const props = withDefaults(defineProps<{ admin?: boolean }>(), { admin: false })
const { t } = useI18n()
const appStore = useAppStore()
const router = useRouter()
const base = computed(() => props.admin ? '/admin/tickets' : '/tickets')
// 两种角色共用同一组展示槽，管理员额外显示用户身份，不改变服务端排序。
const columns = computed<Column[]>(() => [
  { key: 'title', label: t('tickets.subject') },
  ...(props.admin ? [{ key: 'user', label: t('tickets.user') }] : []),
  { key: 'type', label: t('tickets.type') },
  { key: 'priority', label: t('tickets.priority') },
  { key: 'status', label: t('tickets.status') },
  { key: 'updated_at', label: t('tickets.updatedAt') },
  { key: 'actions', label: t('common.actions') },
])
const filters = reactive({ type: '', status: '', priority: '', q: '' })
const filterPanel = ref<HTMLDetailsElement>()
function closeFilters() { if (filterPanel.value) filterPanel.value.open = false }
const items = ref<Ticket[]>([])
const page = ref(1)
const pageSize = ref(20)
const total = ref(0)
const loading = ref(false)
const error = ref('')
const disabled = ref(false)
const settings = ref<TicketSettings>()
const showCreate = ref(false)
const closeTarget = ref<{ ticket: Ticket; action: 'cancel' | 'complete' } | null>(null)
const closing = ref(false)
let requestID = 0
const typeOptions = computed(() => [{ value: '', label: t('tickets.allTypes') }, ...ticketTypes.map(value => ({ value, label: t(`tickets.types.${value}`) }))])
const statusOptions = computed(() => [{ value: '', label: t('tickets.allStatuses') }, ...ticketStatuses.map(value => ({ value, label: t(`tickets.statuses.${value}`) }))])
const priorityOptions = computed(() => [{ value: '', label: t('tickets.allPriorities') }, ...ticketPriorities.map(value => ({ value, label: t(`tickets.priorities.${value}`) }))])

// 已打开的列表同样服从总开关，关闭后撤掉历史数据和所有写入入口。
function markDisabled() {
  ++requestID
  loading.value = false
  disabled.value = true
  void appStore.syncTicketModuleEnabled(false)
  items.value = []
  total.value = 0
  showCreate.value = false
  closeTarget.value = null
  error.value = ''
}
function handleError(err: unknown, fallback: string) {
  if (extractApiErrorCode(err) === 'TICKET_DISABLED') markDisabled()
  else error.value = extractI18nErrorMessage(err, t, 'tickets.errors', t(fallback))
}

async function load() {
  const current = ++requestID
  loading.value = true
  error.value = ''
  try {
    const api = ticketAPI(props.admin)
    const config = await api.config()
    if (current !== requestID) return
    settings.value = config
    if (!config.enabled) { markDisabled(); return }
    void appStore.syncTicketModuleEnabled(true)
    const result = await api.list({ ...filters, q: filters.q.trim(), page: page.value, page_size: pageSize.value })
    // 快速切换筛选时仅采用最后一次请求，防止旧结果覆盖当前条件。
    if (current !== requestID) return
    disabled.value = false
    items.value = result.items
    total.value = result.total
  } catch (err) {
    if (current === requestID) { items.value = []; total.value = 0; handleError(err, 'tickets.loadFailed') }
  } finally { if (current === requestID) loading.value = false }
}
function search() { page.value = 1; void load() }
function refresh() { void load() }
function changePage(value: number) { page.value = value; void load() }
function changePageSize(value: number) { pageSize.value = value; search() }
function created(ticket: Ticket) { showCreate.value = false; void router.push(`${base.value}/${ticket.id}`) }
const isOpen = (ticket: Ticket) => ['pending', 'waiting_user'].includes(ticket.status)
function requestClose(ticket: Ticket, action: 'cancel' | 'complete') {
  if (!closing.value && !disabled.value && isOpen(ticket)) closeTarget.value = { ticket, action }
}
async function closeTicket() {
  if (closing.value || disabled.value || !closeTarget.value) return
  const { ticket, action } = closeTarget.value
  const admin = props.admin
  closing.value = true
  error.value = ''
  // 关单期间作废在途列表请求，防止旧状态覆盖刚完成的操作。
  ++requestID
  loading.value = false
  try {
    await ticketAPI(admin).close(ticket.id, action)
    if (props.admin !== admin) return
    closeTarget.value = null
    await load()
  } catch (err) {
    if (props.admin === admin) handleError(err, 'tickets.actionFailed')
  } finally { closing.value = false }
}
watch(() => props.admin, () => { page.value = 1; settings.value = undefined; disabled.value = false; closeTarget.value = null; void load() })
onMounted(load)
</script>

<style scoped>
/* 手机卡片固定字段标签，长标题和邮箱仅在右侧收缩，不能把标签挤成竖排。 */
@media (max-width: 1023px) {
  .ticket-list :deep([data-field] > span:first-child) { flex-shrink: 0; white-space: nowrap; }
}
</style>
