<template>
  <BaseDialog :show="show" :title="t('tickets.create')" @close="!saving && emit('close')">
    <p v-if="disabled || !settings.enabled" role="status" class="rounded-lg bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-300">{{ t('tickets.disabled') }}</p>
    <form v-else id="create-ticket-form" class="min-w-0 space-y-4 sm:space-y-5" @submit.prevent="submit">
      <p class="rounded-lg bg-primary-50 p-3 text-sm text-primary-700 dark:bg-primary-900/20 dark:text-primary-300">{{ t('tickets.openLimit', { count: settings.max_open_tickets }) }}</p>
      <div class="grid min-w-0 grid-cols-2 gap-3 sm:gap-4">
        <div><label class="input-label" for="ticket-type">{{ t('tickets.type') }}</label><Select id="ticket-type" v-model="form.type" :options="typeOptions" :disabled="saving" /></div>
        <div><label class="input-label" for="ticket-priority">{{ t('tickets.priority') }}</label><Select id="ticket-priority" v-model="form.priority" :options="priorityOptions" :disabled="saving" /></div>
      </div>
      <div v-if="form.type === 'financial'" class="space-y-2">
        <label class="input-label" for="ticket-order">{{ t('tickets.order') }}</label>
        <Select id="ticket-order" v-model="form.order_id" :options="orderOptions" :placeholder="t('tickets.selectOrder')" :disabled="saving || ordersLoading" searchable clearable />
        <p class="text-xs text-gray-500">{{ t('tickets.orderHint') }}</p>
        <p v-if="!ordersLoading && !orders.length && !ordersError" class="text-sm text-gray-500">{{ t('tickets.noOrders') }}</p>
        <p v-if="ordersError" role="alert" class="text-sm text-red-600">{{ ordersError }}</p>
        <button v-if="orders.length < orderTotal || ordersError" type="button" class="btn btn-secondary btn-sm" :disabled="ordersLoading" @click="loadOrders">{{ ordersLoading ? t('common.loading') : ordersError ? t('tickets.retry') : t('tickets.moreOrders') }}</button>
      </div>
      <div>
        <label class="input-label" for="ticket-title">{{ t('tickets.subject') }}</label>
        <input id="ticket-title" v-model="form.title" type="text" required :disabled="saving" class="input w-full" :placeholder="t('tickets.subjectPlaceholder')" :aria-invalid="titleTooLong || undefined" aria-describedby="ticket-title-hint ticket-title-count" @compositionstart="titleComposing = true" @compositionend="titleComposing = false" />
        <div class="mt-1 flex justify-between gap-3 text-xs" :class="titleTooLong ? 'text-red-600' : 'text-gray-500'"><p id="ticket-title-hint">{{ t('tickets.titleLimit', { max: ticketTitleMaxLength }) }}</p><span id="ticket-title-count" class="shrink-0 tabular-nums">{{ titleLength }} / {{ ticketTitleMaxLength }}</span></div>
        <p v-if="titleTooLong" role="alert" class="mt-1 text-sm text-red-600">{{ t('tickets.errors.TICKET_TITLE_TOO_LONG', { max: ticketTitleMaxLength }) }}</p>
      </div>
      <div><label class="input-label" for="ticket-content">{{ t('tickets.content') }}</label><textarea id="ticket-content" v-model="form.content" required maxlength="20000" rows="6" :disabled="saving" class="input block w-full resize-y" :placeholder="t('tickets.contentPlaceholder')" /></div>
      <TicketAttachments v-model="files" :settings="settings" :disabled="saving" />
      <p v-if="error" role="alert" class="text-sm text-red-600">{{ error }}</p>
    </form>
    <template #footer>
      <!-- 手机底部操作均分可触达宽度，桌面保持原有右对齐。 -->
      <div class="grid grid-cols-2 gap-3 sm:flex sm:justify-end"><button type="button" class="btn btn-secondary" :disabled="saving" @click="emit('close')">{{ t('common.cancel') }}</button><button type="submit" form="create-ticket-form" class="btn btn-primary" :disabled="saving || disabled || !settings.enabled || titleComposing || titleTooLong || !form.title.trim() || !form.content.trim()">{{ saving ? t('common.processing') : t('tickets.create') }}</button></div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, reactive, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import TicketAttachments from './TicketAttachments.vue'
import { ticketAPI, ticketTypes, ticketPriorities, ticketTitleMaxLength, type Ticket, type TicketSettings, type TicketType, type TicketPriority } from '@/api/tickets'
import { paymentAPI } from '@/api/payment'
import type { PaymentOrder } from '@/types/payment'
import { extractApiErrorCode, extractI18nErrorMessage } from '@/utils/apiError'
import { ticketSubmission } from './submission'

const props = defineProps<{ show: boolean; settings: TicketSettings }>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'created', ticket: Ticket): void; (e: 'disabled'): void }>()
const { t } = useI18n()
const form = reactive({ type: 'consultation' as TicketType, priority: 'normal' as TicketPriority, title: '', content: '', order_id: null as number | null })
const files = ref<File[]>([])
const saving = ref(false)
const error = ref('')
const disabled = ref(false)
const titleComposing = ref(false)
// 不使用按 UTF-16 计数的原生 maxlength，避免把 emoji 错算为两个字。
const titleLength = computed(() => Array.from(form.title.trim()).length)
const titleTooLong = computed(() => titleLength.value > ticketTitleMaxLength)
const submission = ticketSubmission()
const orders = ref<PaymentOrder[]>([])
const ordersLoading = ref(false)
const ordersError = ref('')
const orderTotal = ref(0)
let orderPage = 0
const typeOptions = computed(() => ticketTypes.map(value => ({ value, label: t(`tickets.types.${value}`) })))
const priorityOptions = computed(() => ticketPriorities.map(value => ({ value, label: t(`tickets.priorities.${value}`) })))
const orderOptions = computed(() => orders.value.map(order => ({ value: order.id, label: `${order.out_trade_no || `#${order.id}`} · ${order.currency || 'CNY'} ${order.pay_amount.toFixed(2)} · ${new Date(order.created_at).toLocaleDateString()}` })))

async function loadOrders() {
  if (ordersLoading.value) return
  ordersLoading.value = true
  ordersError.value = ''
  try {
    const { data } = await paymentAPI.getMyOrders({ page: orderPage + 1, page_size: 50 })
    orders.value.push(...data.items)
    orderTotal.value = data.total
    orderPage += 1
  } catch (err) {
    ordersError.value = extractI18nErrorMessage(err, t, 'tickets.errors', t('tickets.loadOrdersFailed'))
  } finally { ordersLoading.value = false }
}

watch(() => form.type, value => {
  // 切出财务立即清除关联，不能把隐藏的旧订单提交给其它类型。
  if (value !== 'financial') form.order_id = null
  else if (orderPage === 0) void loadOrders()
})
watch(() => props.show, show => {
  if (!show) return
  Object.assign(form, { type: 'consultation', priority: 'normal', title: '', content: '', order_id: null })
  files.value = []
  error.value = ''
  disabled.value = false
  titleComposing.value = false
  submission.reset()
})

async function submit() {
  // 提交入口再次校验，程序触发表单也不能绕过标题上限或提交输入法合成中间态。
  if (saving.value || disabled.value || !props.settings.enabled || titleComposing.value || titleTooLong.value || !form.title.trim() || !form.content.trim()) return
  saving.value = true
  error.value = ''
  try {
    const payload = { type: form.type, priority: form.priority, title: form.title.trim(), content: form.content.trim(), ...(form.type === 'financial' && form.order_id ? { order_id: form.order_id } : {}) }
    const ticket = await ticketAPI().create(payload, files.value, submission.key(payload, files.value))
    submission.reset()
    emit('created', ticket)
  } catch (err) {
    if (extractApiErrorCode(err) === 'TICKET_DISABLED') {
      // 服务端关闭后通知列表收起入口，避免使用旧配置再次提交。
      disabled.value = true
      emit('disabled')
    } else error.value = extractI18nErrorMessage(err, t, 'tickets.errors', t('tickets.createFailed'))
  } finally { saving.value = false }
}
</script>
