<template>
  <AppLayout full-viewport>
    <!-- 页面高度固定，对话独立滚动，回复区域始终处于可访问位置。 -->
    <div class="ticket-workspace mx-auto flex min-h-0 min-w-0 max-w-5xl flex-col gap-3 overflow-hidden">
      <div class="ticket-toolbar flex min-w-0 shrink-0 items-center justify-between gap-3"><RouterLink :to="base" class="min-w-0 truncate py-2 text-sm text-primary-600 hover:underline dark:text-primary-400">← {{ t('tickets.back') }}</RouterLink><button type="button" class="btn btn-secondary btn-sm shrink-0" :disabled="loading || busy" @click="load">{{ t('common.refresh') }}</button></div>
      <p v-if="disabled" role="status" class="shrink-0 rounded-xl bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-300">{{ t('tickets.disabled') }}</p>
      <p v-if="error" role="alert" class="max-h-24 shrink-0 overflow-y-auto break-words rounded-xl bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ error }}</p>
      <div v-if="loading && !ticket" class="p-12 text-center text-gray-500" role="status">{{ t('common.loading') }}</div>
      <template v-if="ticket">
        <section class="ticket-info card min-w-0 shrink-0 p-3 sm:p-4">
          <!-- 手机端为标题和状态分别留出整行，沿用账号管理的紧凑卡片间距。 -->
          <div class="ticket-title-row flex min-w-0 flex-col gap-2 sm:flex-row sm:items-start sm:justify-between">
            <div class="ticket-title-block w-full min-w-0 flex-1 sm:w-auto">
              <p class="text-xs text-gray-500">#{{ ticket.id }} · {{ t(`tickets.types.${ticket.type}`) }}</p>
              <h1 class="mt-1 truncate text-base font-semibold text-gray-900 dark:text-white sm:text-lg" :title="ticket.title">{{ ticket.title }}</h1>
            </div>
            <div class="ticket-badges flex min-w-0 flex-wrap gap-2 sm:shrink-0"><TicketBadge kind="priority" :value="ticket.priority" /><TicketBadge kind="status" :value="ticket.status" :closed-by-role="ticket.closed_by_role" /></div>
          </div>
          <dl class="ticket-metadata mt-3 grid min-w-0 gap-1.5 text-xs sm:grid-cols-3 sm:gap-2">
            <div v-if="admin" class="min-w-0"><dt class="text-gray-500">{{ t('tickets.user') }}</dt><dd class="mt-1 min-w-0 truncate" :title="`${ticket.user_name} · ${ticket.user_email}`">{{ ticket.user_name }} · {{ ticket.user_email }}</dd></div>
            <div class="min-w-0"><dt class="text-gray-500">{{ t('tickets.createdAt') }}</dt><dd class="mt-1 min-w-0 truncate" :title="formatDate(ticket.created_at)">{{ formatDate(ticket.created_at) }}</dd></div>
            <div v-if="ticket.order_id" class="min-w-0"><dt class="text-gray-500">{{ t('tickets.order') }}</dt><dd class="ticket-order-summary mt-1 flex min-w-0 gap-x-2" :title="`${ticket.order?.out_trade_no || `#${ticket.order_id}`}${ticket.order ? ` · ${ticket.order.currency || 'CNY'} ${ticket.order.pay_amount.toFixed(2)}` : ''}`"><span class="min-w-0 truncate" :title="ticket.order?.out_trade_no || `#${ticket.order_id}`">{{ ticket.order?.out_trade_no || `#${ticket.order_id}` }}</span><span v-if="ticket.order" class="shrink-0">{{ ticket.order.currency || 'CNY' }} {{ ticket.order.pay_amount.toFixed(2) }}</span></dd></div>
          </dl>
          <div v-if="isOpen" class="ticket-actions mt-3 flex min-w-0 flex-wrap items-center gap-2 border-t border-gray-100 pt-3 dark:border-dark-700 sm:gap-3">
            <div v-if="admin" class="ticket-priority-control flex min-w-0 items-center gap-2"><label for="detail-priority" class="sr-only shrink-0 text-sm text-gray-500 sm:not-sr-only">{{ t('tickets.priority') }}</label><Select id="detail-priority" class="w-24 sm:w-28" :aria-label="t('tickets.priority')" :model-value="ticket.priority" :options="priorityOptions" :disabled="busy" @update:model-value="changePriority" /></div>
            <div class="ticket-close-actions flex min-w-0 flex-1 justify-end gap-2"><button type="button" class="btn btn-secondary btn-sm" :disabled="busy || loading" @click="closeAction = 'cancel'">{{ t('tickets.cancel') }}</button><button type="button" class="btn btn-primary btn-sm" :disabled="busy || loading" @click="closeAction = 'complete'">{{ t('tickets.complete') }}</button></div>
          </div>
        </section>
        <section class="ticket-thread flex min-h-0 min-w-0 flex-1 flex-col gap-2" :aria-label="t('tickets.conversation')">
          <div class="ticket-conversation-heading flex shrink-0 items-center justify-between gap-2"><h2 id="ticket-conversation-heading" class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('tickets.conversation') }}</h2><button v-if="!followLatest" type="button" class="text-xs text-primary-600 hover:underline dark:text-primary-400" @click="scrollToLatest">{{ t('tickets.latest') }}</button></div>
          <div ref="conversationElement" data-testid="ticket-messages" role="region" tabindex="0" aria-labelledby="ticket-conversation-heading" class="min-h-0 min-w-0 flex-1 space-y-3 overflow-x-hidden overflow-y-auto overscroll-contain pr-1" @scroll="trackConversationScroll">
          <article v-for="message in ticket.messages || []" :key="message.id" class="min-w-0 rounded-xl border p-3 sm:rounded-2xl sm:p-5" :class="message.is_staff ? 'border-primary-100 bg-primary-50/50 dark:border-primary-900/50 dark:bg-primary-900/10' : 'border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900'">
            <div class="flex min-w-0 flex-wrap items-center justify-between gap-2 text-xs"><div class="flex min-w-0 max-w-full items-center gap-2"><span class="min-w-0 truncate font-semibold text-gray-900 dark:text-white" :title="message.sender_name || (message.is_staff ? t('tickets.staff') : ticket.user_name || t('tickets.user'))">{{ message.sender_name || (message.is_staff ? t('tickets.staff') : ticket.user_name || t('tickets.user')) }}</span><span v-if="message.is_staff" class="shrink-0 rounded bg-primary-100 px-2 py-0.5 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300">{{ t('tickets.staff') }}</span></div><time :datetime="message.created_at" class="text-gray-500">{{ formatDate(message.created_at) }}</time></div>
            <!-- 对话内容作为纯文本展示，避免上传者把消息转为可执行 HTML。 -->
            <p class="mt-3 whitespace-pre-wrap break-words text-sm leading-6 text-gray-800 dark:text-gray-200 sm:mt-4 sm:leading-7">{{ message.content }}</p>
            <ul v-if="message.attachments?.length" class="mt-3 flex min-w-0 flex-wrap gap-2 sm:mt-4"><li v-for="attachment in message.attachments" :key="attachment.id" class="min-w-0 max-w-full"><button type="button" class="flex min-w-0 max-w-full items-center gap-2 rounded-lg border border-gray-200 bg-white px-3 py-2 text-xs text-primary-600 hover:bg-gray-50 dark:border-dark-600 dark:bg-dark-800 dark:text-primary-400" :aria-label="t('tickets.preview.openNamed', { name: attachment.filename })" @click="previewAttachment = attachment"><Icon name="document" size="sm" class="shrink-0" /><span class="min-w-0 max-w-64 truncate" :title="attachment.filename">{{ attachment.filename }}</span><span class="shrink-0 text-gray-500">{{ formatAttachmentSize(attachment.size) }}</span></button></li></ul>
          </article>
          </div>
        </section>
        <form v-if="isOpen && settings" data-testid="ticket-composer" class="ticket-composer card min-h-0 min-w-0 max-h-[40dvh] shrink-0 space-y-2 overflow-x-hidden overflow-y-auto p-3 sm:p-4" @submit.prevent="reply">
          <label class="ticket-reply-label input-label" for="ticket-reply">{{ t('tickets.reply') }}</label><textarea id="ticket-reply" v-model="content" rows="3" maxlength="20000" class="input block w-full resize-none" :disabled="busy" :placeholder="t('tickets.replyPlaceholder')" />
          <details class="ticket-reply-attachments"><summary class="cursor-pointer text-xs text-primary-600 dark:text-primary-400">{{ t('tickets.attachments') }}{{ files.length ? ` (${files.length})` : '' }}</summary><TicketAttachments id="reply-attachments" v-model="files" class="mt-2" :settings="settings" :disabled="busy" /></details>
          <div class="ticket-reply-actions flex min-w-0 items-center justify-between gap-3"><p v-if="!admin && settings.auto_expire_hours > 0" class="ticket-expiry-hint min-w-0 text-xs text-gray-500">{{ t('tickets.expiryHint', { hours: settings.auto_expire_hours }) }}</p><button type="submit" class="btn btn-primary ml-auto shrink-0" :disabled="busy || loading || (!content.trim() && !files.length)">{{ busy ? t('common.processing') : t('tickets.sendReply') }}</button></div>
        </form>
        <p v-else-if="!isOpen" class="shrink-0 rounded-xl bg-gray-100 p-3 text-sm text-gray-500 dark:bg-dark-800">{{ t('tickets.closedHint') }}</p>
      </template>
    </div>
    <ConfirmDialog :show="!!closeAction" :title="t(closeAction === 'cancel' ? 'tickets.cancel' : 'tickets.complete')" :message="t(closeAction === 'cancel' ? 'tickets.cancelConfirm' : 'tickets.completeConfirm')" :danger="closeAction === 'cancel'" :loading="busy" @cancel="!busy && (closeAction = null)" @confirm="closeTicket" />
    <!-- 预览复用站内弹窗，关闭时卸载内容，保留工单草稿、滚动位置并释放附件资源。 -->
    <BaseDialog :show="!!previewAttachment" :title="previewAttachment?.filename || t('tickets.preview.title')" width="extra-wide" @close="previewAttachment = undefined">
      <TicketAttachmentPreview v-if="previewAttachment && ticket" :ticket-id="ticket.id" :attachment-id="previewAttachment.id" :admin="admin" :show-title="false" @disabled="markDisabled" />
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, computed, watch, onMounted, nextTick } from 'vue'
import { RouterLink, useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Select from '@/components/common/Select.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import TicketAttachmentPreview from '@/components/tickets/TicketAttachmentPreview.vue'
import Icon from '@/components/icons/Icon.vue'
import TicketBadge from '@/components/tickets/TicketBadge.vue'
import TicketAttachments from '@/components/tickets/TicketAttachments.vue'
import { formatAttachmentSize } from '@/components/tickets/attachments'
import { ticketAPI, ticketPriorities, type Ticket, type TicketSettings, type TicketPriority, type TicketAttachment } from '@/api/tickets'
import { extractApiErrorCode, extractI18nErrorMessage } from '@/utils/apiError'
import { useAppStore } from '@/stores/app'
import { ticketSubmission } from '@/components/tickets/submission'

const props = withDefaults(defineProps<{ admin?: boolean }>(), { admin: false })
const { t } = useI18n()
const appStore = useAppStore()
const route = useRoute()
const base = computed(() => props.admin ? '/admin/tickets' : '/tickets')
const ticket = ref<Ticket>()
const settings = ref<TicketSettings>()
const loading = ref(false)
const busy = ref(false)
const error = ref('')
const disabled = ref(false)
const content = ref('')
const files = ref<File[]>([])
const closeAction = ref<'cancel' | 'complete' | null>(null)
const previewAttachment = ref<TicketAttachment>()
const conversationElement = ref<HTMLElement>()
const followLatest = ref(true)
const submission = ticketSubmission()
const isOpen = computed(() => ticket.value && ['pending', 'waiting_user'].includes(ticket.value.status))
const priorityOptions = computed(() => ticketPriorities.map(value => ({ value, label: t(`tickets.priorities.${value}`) })))
const formatDate = (date: string) => new Date(date).toLocaleString()
let requestID = 0

function trackConversationScroll() {
  const element = conversationElement.value
  if (element) followLatest.value = element.scrollHeight - element.scrollTop - element.clientHeight < 80
}
async function scrollToLatest() {
  followLatest.value = true
  await nextTick()
  const element = conversationElement.value
  if (element) element.scrollTop = element.scrollHeight
}

// 总开关关闭后清除旧详情和确认框，不能留下仍可操作的历史界面。
function markDisabled() {
  ++requestID
  loading.value = false
  disabled.value = true
  void appStore.syncTicketModuleEnabled(false)
  ticket.value = undefined
  closeAction.value = null
  previewAttachment.value = undefined
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
    const result = await api.get(Number(route.params.id))
    if (current === requestID) {
      disabled.value = false
      // 刷新时尊重用户正在浏览历史的位置，首次进入或已在底部才跟随最新消息。
      const shouldFollow = !ticket.value || followLatest.value
      ticket.value = result; settings.value = config
      if (shouldFollow) await scrollToLatest()
    }
  } catch (err) {
    if (current === requestID) handleError(err, 'tickets.loadFailed')
  } finally { if (current === requestID) loading.value = false }
}

async function mutate(action: () => Promise<Ticket>): Promise<boolean> {
  // 刷新读请求完成前不接受写入，避免旧详情覆盖刚提交的回复。
  if (busy.value || loading.value || disabled.value) return false
  busy.value = true
  error.value = ''
  const current = requestID
  try { const result = await action(); if (current !== requestID) return false; ticket.value = result; return true }
  catch (err) { if (current === requestID) handleError(err, 'tickets.actionFailed'); return false }
  finally { busy.value = false }
}
async function reply() {
  if (!ticket.value || (!content.value.trim() && !files.value.length) || !isOpen.value) return
  const id = ticket.value.id
  const body = content.value.trim()
  const key = submission.key({ id, content: body }, files.value)
  if (await mutate(() => ticketAPI(props.admin).reply(id, body, files.value, key))) {
    content.value = ''; files.value = []; submission.reset()
    await scrollToLatest()
  }
}
async function changePriority(value: string | number | boolean | null) {
  if (!ticket.value || !ticketPriorities.includes(value as TicketPriority) || value === ticket.value.priority) return
  await mutate(() => ticketAPI(true).update(ticket.value!.id, { priority: value as TicketPriority }))
}
async function closeTicket() {
  if (!ticket.value || !closeAction.value) return
  const action = closeAction.value
  if (await mutate(() => ticketAPI(props.admin).close(ticket.value!.id, action))) closeAction.value = null
}
watch(() => [route.params.id, props.admin], () => {
  // 切换工单清空原对话和草稿，避免异步返回混入另一位用户的会话。
  ticket.value = undefined; settings.value = undefined; content.value = ''; files.value = []; closeAction.value = null
  previewAttachment.value = undefined
  followLatest.value = true
  disabled.value = false
  submission.reset()
  void load()
})
onMounted(load)
</script>

<style scoped>
/* 扣除公共顶栏和内容内边距，消息数量不会继续撑高根页面。 */
.ticket-workspace {
  height: calc(100dvh - 5.5rem);
}
/* 长消息只在卡片内换行，附件文件名与发送者不能撑宽手机页面。 */
.ticket-thread article p { overflow-wrap: anywhere; }
.ticket-reply-attachments { min-width: 0; }
@media (max-width: 639px) {
  .ticket-workspace { gap: 0.5rem; }
  .ticket-toolbar { min-height: 2.25rem; }
  .ticket-info { padding: 0.75rem; }
  .ticket-metadata { margin-top: 0.5rem; }
  .ticket-metadata > div { display: flex; min-width: 0; align-items: baseline; gap: 0.5rem; }
  .ticket-metadata dt { flex-shrink: 0; }
  .ticket-metadata dd { margin-top: 0; }
  .ticket-actions { margin-top: 0.5rem; padding-top: 0.5rem; }
  .ticket-actions .btn { min-height: 2.25rem; padding-right: 0.625rem; padding-left: 0.625rem; }
  .ticket-priority-control { flex: 0 0 auto; }
  .ticket-composer .btn { min-height: 2.25rem; }
  #ticket-reply { font-size: 1rem; }
}
/* 较矮手机收紧回复框高度，但保留正文、附件入口和发送操作。 */
@media (max-width: 639px) and (max-height: 700px) {
  #ticket-reply { height: 4rem; min-height: 4rem; }
  .ticket-composer { padding: 0.625rem; }
  .ticket-reply-label { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; }
}
@media (min-width: 768px) {
  .ticket-workspace { height: calc(100dvh - 6.25rem); }
}
@media (min-width: 1024px) {
  .ticket-workspace { height: calc(100dvh - 6.5rem); }
}
/* 小尺寸手机、软键盘和横屏均收紧布局，568px 高度也必须留出可阅读的对话。 */
@media (max-height: 550px), (max-width: 639px) and (max-height: 700px) {
  .ticket-workspace { gap: 0.375rem; }
  /* 附件展开也必须保留对话高度；信息和回复在各自区域滚动，不挤走消息。 */
  .ticket-thread { min-height: 6.75rem; }
  .ticket-info { min-height: 0; flex-shrink: 1; overflow-x: hidden; overflow-y: auto; overscroll-behavior: contain; padding: 0.5rem; }
  .ticket-info h1 { margin-top: 0; font-size: 1rem; }
  .ticket-title-block { display: flex; min-width: 0; align-items: center; gap: 0.375rem; }
  .ticket-title-block > p { flex-shrink: 0; }
  .ticket-metadata { display: flex; flex-wrap: wrap; gap: 0.25rem 0.75rem; margin-top: 0.25rem; }
  .ticket-metadata > div { display: flex; align-items: baseline; gap: 0.25rem; max-width: 100%; }
  .ticket-metadata dt { flex-shrink: 0; }
  .ticket-metadata dd { margin-top: 0; }
  .ticket-actions { gap: 0.5rem; margin-top: 0.25rem; padding-top: 0.25rem; }
  .ticket-composer { display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: 0.25rem 0.5rem; min-height: 6.125rem; max-height: 40%; flex-shrink: 1; overscroll-behavior: contain; padding: 0.5rem; }
  .ticket-composer > * { margin-top: 0 !important; }
  #ticket-reply { grid-column: 1 / -1; height: 2.5rem; min-height: 2.5rem; }
  .ticket-reply-attachments[open] { grid-column: 1 / -1; }
  .ticket-reply-actions { justify-self: end; }
  .ticket-expiry-hint { display: none; }
  .ticket-reply-label { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; }
}
/* 软键盘压缩窄屏时，信息区仍直接展开并可独立滚动，为对话与回复保留空间。 */
@media (max-height: 550px) and (max-width: 639px) {
  .ticket-info { min-height: 0; max-height: 38%; flex-shrink: 1; overflow-x: hidden; overflow-y: auto; overscroll-behavior: contain; }
  .ticket-title-block { display: block; }
  .ticket-title-row { gap: 0.25rem; }
  .ticket-metadata { display: grid; }
  .ticket-metadata > div { min-width: 0; }
  .ticket-composer { max-height: 38%; }
}
/* 横屏把操作排在信息旁边，展开内容也不会把消息区和回复区推出视口。 */
@media (max-height: 550px) and (min-width: 640px) {
  .ticket-info { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 0.25rem 0.75rem; }
  .ticket-title-row, .ticket-metadata { grid-column: 1; }
  /* 元信息固定为一排三列，长邮箱和订单号只在各列内省略，不继续增加信息卡高度。 */
  .ticket-metadata { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 0.5rem; margin-top: 0; }
  .ticket-metadata > div { display: block; min-width: 0; }
  .ticket-metadata dd { display: block; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .ticket-metadata .ticket-order-summary { display: flex; flex-wrap: nowrap; gap: 0.25rem; }
  .ticket-order-summary > span:first-child { min-width: 0; }
  .ticket-actions { grid-column: 2; grid-row: 1 / span 2; align-self: center; margin: 0; padding: 0; border: 0; }
  .ticket-priority-control label { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; }
  /* 横屏的输入和发送同排，附件展开后占第二排并在回复区内滚动。 */
  .ticket-composer { grid-template-columns: minmax(0, 1fr) auto auto; min-height: 3.75rem; }
  #ticket-reply { grid-column: 1; grid-row: 1; }
  .ticket-reply-attachments { grid-column: 2; grid-row: 1; }
  .ticket-reply-actions { grid-column: 3; grid-row: 1; }
  .ticket-reply-attachments[open] { grid-column: 1 / -1; grid-row: 2; }
}
@media (max-height: 400px) {
  .ticket-conversation-heading { display: none; }
  .ticket-thread { min-height: 5rem; }
}
</style>
