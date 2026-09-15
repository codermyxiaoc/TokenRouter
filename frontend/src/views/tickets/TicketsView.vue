<template>
  <AppLayout>
    <template #page-heading-actions>
      <div class="flex gap-2"><button type="button" class="btn btn-secondary" :disabled="loading || closing" @click="refresh">{{ t('common.refresh') }}</button><button v-if="!admin && !disabled" type="button" class="btn btn-primary" :disabled="!settings?.enabled" @click="showCreate = true">{{ t('tickets.create') }}</button></div>
    </template>
    <div class="space-y-6">
      <p v-if="disabled" role="status" class="rounded-xl bg-amber-50 p-4 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-300">{{ t('tickets.disabled') }}</p>
      <form v-if="!disabled" class="flex flex-wrap items-end gap-3" @submit.prevent="search">
        <div class="w-36"><label class="input-label" for="filter-ticket-type">{{ t('tickets.type') }}</label><Select id="filter-ticket-type" v-model="filters.type" :options="typeOptions" @change="search" /></div>
        <div class="w-36"><label class="input-label" for="filter-ticket-status">{{ t('tickets.status') }}</label><Select id="filter-ticket-status" v-model="filters.status" :options="statusOptions" @change="search" /></div>
        <div class="w-36"><label class="input-label" for="filter-ticket-priority">{{ t('tickets.priority') }}</label><Select id="filter-ticket-priority" v-model="filters.priority" :options="priorityOptions" @change="search" /></div>
        <div class="min-w-48 flex-1"><label class="input-label" for="filter-ticket-query">{{ t('tickets.search') }}</label><input id="filter-ticket-query" v-model="filters.q" type="search" maxlength="200" class="input w-full" :placeholder="t('tickets.searchPlaceholder')" /></div>
        <button type="submit" class="btn btn-secondary">{{ t('common.search') }}</button>
      </form>
      <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
      <div v-if="!disabled" class="card overflow-hidden">
        <div v-if="loading" class="p-12 text-center text-gray-500" role="status">{{ t('common.loading') }}</div>
        <div v-else-if="!items.length" class="p-12 text-center"><p class="font-medium text-gray-900 dark:text-white">{{ t('tickets.empty') }}</p><p class="mt-2 text-sm text-gray-500">{{ t(admin ? 'tickets.emptyAdminHint' : 'tickets.emptyHint') }}</p></div>
        <div v-else class="overflow-x-auto">
          <table class="w-full text-left text-sm"><thead class="bg-gray-50 text-xs text-gray-500 dark:bg-dark-800"><tr><th class="px-5 py-3">{{ t('tickets.subject') }}</th><th v-if="admin" class="px-4 py-3">{{ t('tickets.user') }}</th><th class="px-4 py-3">{{ t('tickets.type') }}</th><th class="px-4 py-3">{{ t('tickets.priority') }}</th><th class="px-4 py-3">{{ t('tickets.status') }}</th><th class="px-4 py-3">{{ t('tickets.updatedAt') }}</th><th class="px-4 py-3">{{ t('common.actions') }}</th></tr></thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700"><tr v-for="ticket in items" :key="ticket.id" class="hover:bg-gray-50 dark:hover:bg-dark-800/50"><td class="max-w-sm px-5 py-4"><RouterLink :to="`${base}/${ticket.id}`" :title="ticket.title" class="block max-w-64 truncate font-medium sm:max-w-sm text-primary-600 hover:underline dark:text-primary-400">{{ ticket.title }}</RouterLink><span class="mt-1 block text-xs text-gray-400">#{{ ticket.id }}</span></td><td v-if="admin" class="px-4 py-4"><p>{{ ticket.user_name }}</p><p class="text-xs text-gray-500">{{ ticket.user_email }}</p></td><td class="whitespace-nowrap px-4 py-4">{{ t(`tickets.types.${ticket.type}`) }}</td><td class="px-4 py-4"><TicketBadge kind="priority" :value="ticket.priority" /></td><td class="whitespace-nowrap px-4 py-4"><TicketBadge kind="status" :value="ticket.status" :closed-by-role="ticket.closed_by_role" /></td><td class="whitespace-nowrap px-4 py-4 text-xs text-gray-500">{{ new Date(ticket.updated_at).toLocaleString() }}</td><td class="whitespace-nowrap px-4 py-4"><div v-if="isOpen(ticket)" class="flex gap-2"><button type="button" class="btn btn-secondary btn-sm" :disabled="closing" @click="requestClose(ticket, 'complete')">{{ t('tickets.complete') }}</button><button type="button" class="btn btn-secondary btn-sm" :disabled="closing" @click="requestClose(ticket, 'cancel')">{{ t('tickets.cancel') }}</button></div><span v-else class="text-gray-400">—</span></td></tr></tbody>
          </table>
        </div>
        <Pagination v-if="total > 0" :page="page" :page-size="pageSize" :total="total" @update:page="changePage" @update:page-size="changePageSize" />
      </div>
    </div>
    <CreateTicketDialog v-if="settings && !admin && !disabled" :show="showCreate" :settings="settings" @close="showCreate = false" @created="created" @disabled="markDisabled" />
    <ConfirmDialog :show="!!closeTarget" :title="t(closeTarget?.action === 'cancel' ? 'tickets.cancel' : 'tickets.complete')" :message="t(closeTarget?.action === 'cancel' ? 'tickets.cancelConfirm' : 'tickets.completeConfirm')" :danger="closeTarget?.action === 'cancel'" :loading="closing" @cancel="!closing && (closeTarget = null)" @confirm="closeTicket"><p class="break-words text-sm text-gray-500">{{ closeTarget?.ticket.title }}</p></ConfirmDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, reactive, computed, onMounted, watch } from 'vue'
import { RouterLink, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
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
const filters = reactive({ type: '', status: '', priority: '', q: '' })
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
