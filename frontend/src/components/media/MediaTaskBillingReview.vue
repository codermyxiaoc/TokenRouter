<template>
  <section v-if="review" class="mt-5 rounded-lg border border-primary-200 p-4 dark:border-primary-800" data-testid="billing-review">
    <h3 class="font-medium text-gray-900 dark:text-gray-100">{{ t('mediaTasks.billingReview.title') }}</h3>
    <p class="mt-2 text-sm text-gray-600 dark:text-gray-300">{{ t(`mediaTasks.billingReview.hints.${review.status}`) }}</p>
    <dl class="mt-4 grid grid-cols-1 gap-3 text-sm sm:grid-cols-2">
      <div v-for="field in fields" :key="field.key" class="min-w-0">
        <dt class="text-xs text-gray-500">{{ t(`mediaTasks.billingReview.${field.key}`) }}</dt>
        <dd class="mt-1 break-all text-gray-900 dark:text-gray-100">{{ field.value }}</dd>
      </div>
    </dl>
    <p v-if="success" role="status" class="mt-3 text-sm text-green-700 dark:text-green-300">{{ success }}</p>
    <template v-if="admin && review.status === 'pending' && (review.can_release || review.can_charge)">
      <div v-if="!action" class="mt-4 flex flex-wrap gap-2">
        <button v-if="review.can_release" type="button" class="btn btn-secondary" data-testid="billing-release" @click="chooseAction('release')">{{ t('mediaTasks.billingReview.release') }}</button>
        <button v-if="review.can_charge" type="button" class="btn btn-primary" data-testid="billing-charge" @click="chooseAction('charge')">{{ t('mediaTasks.billingReview.charge') }}</button>
      </div>
      <form v-else class="mt-4 space-y-3 rounded-lg bg-gray-50 p-4 dark:bg-dark-800" data-testid="billing-confirmation" @submit.prevent="submit">
        <p class="font-medium text-gray-900 dark:text-white">{{ t(`mediaTasks.billingReview.${action}Confirm`) }}</p>
        <p class="break-all text-xs text-gray-500">{{ task.task_id }}</p>
        <p class="text-sm text-gray-700 dark:text-gray-300">{{ t(`mediaTasks.billingReview.${action}Amount`, { amount: amount(review.original_reserved_amount) }) }}</p>
        <label class="input-label" for="media-settlement-reason">{{ t('mediaTasks.billingReview.reason') }}</label>
        <textarea id="media-settlement-reason" v-model="reason" rows="3" maxlength="1000" class="input resize-y" :disabled="busy" :placeholder="t('mediaTasks.billingReview.reasonPlaceholder')" data-testid="billing-reason" />
        <p v-if="error" role="alert" class="text-sm text-red-600 dark:text-red-400">{{ error }}</p>
        <div class="flex flex-wrap gap-2">
          <button type="submit" class="btn btn-primary" :disabled="busy" data-testid="billing-confirm">{{ busy ? t('common.loading') : t('common.confirm') }}</button>
          <button type="button" class="btn btn-secondary" :disabled="busy" @click="reset">{{ t('common.cancel') }}</button>
        </div>
      </form>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminMediaTasksAPI, type MediaTask, type MediaTaskSettlementRequest } from '@/api/mediaTasks'
import { extractApiErrorMessage } from '@/utils/apiError'
import { formatVideoTaskAmount } from './mediaTaskCost'

const props = defineProps<{ task: MediaTask; admin?: boolean }>()
const emit = defineEmits<{ settled: [task: MediaTask] }>()
const { t } = useI18n()
const review = computed(() => props.task.billing_review)
const action = ref<MediaTaskSettlementRequest['action'] | null>(null)
const reason = ref('')
const busy = ref(false)
const error = ref('')
const success = ref('')
let request = 0
const amount = (value: number | undefined) => formatVideoTaskAmount(value) ?? '—'
const time = (value?: string) => value ? new Date(value).toLocaleString() : '—'
const fields = computed(() => {
  const b = review.value
  if (!b) return []
  return [
    { key: 'status', value: t(`mediaTasks.billingReview.statuses.${b.status}`) },
    { key: 'heldBalance', value: b.status === 'unavailable' ? '—' : amount(b.held_balance_amount) },
    { key: 'heldSubscription', value: b.status === 'unavailable' ? '—' : amount(b.held_subscription_amount) },
    { key: 'originalReserved', value: b.status === 'unavailable' ? '—' : amount(b.original_reserved_amount) },
    ...(b.actual_amount != null ? [{ key: 'actual', value: amount(b.actual_amount) }] : []),
    ...(b.expires_at ? [{ key: 'deadline', value: time(b.expires_at) }] : []),
    ...(b.resolved_at ? [{ key: 'resolvedAt', value: time(b.resolved_at) }] : []),
    ...(b.action ? [{ key: 'action', value: t(`mediaTasks.billingReview.actions.${b.action}`) }] : []),
    ...(props.admin && b.actor_id ? [{ key: 'actor', value: `#${b.actor_id}` }] : []),
    ...(props.admin && b.reason ? [{ key: 'reason', value: b.reason }] : []),
  ]
})

function reset() { action.value = null; reason.value = ''; error.value = '' }
function chooseAction(value: MediaTaskSettlementRequest['action']) { reset(); success.value = ''; action.value = value }
async function submit() {
  const b = review.value
  // 按服务端能力及页面权限双重约束；失败状态本身不能证明存在可退款资金。
  if (busy.value || !props.admin || !b || b.status !== 'pending' || !action.value ||
      (action.value === 'charge' ? !b.can_charge : !b.can_release)) return
  const trimmedReason = reason.value.trim()
  if (Array.from(trimmedReason).length < 1 || Array.from(trimmedReason).length > 500) {
    error.value = t('mediaTasks.billingReview.reasonInvalid')
    return
  }
  const current = ++request
  busy.value = true
  error.value = ''
  try {
    const result = await adminMediaTasksAPI.settle(props.task.id, { action: action.value, reason: trimmedReason })
    // 关闭详情、切换任务或角色之后，旧请求不得写入新的详情。
    if (current !== request) return
    reset()
    success.value = t('mediaTasks.billingReview.saved')
    emit('settled', result)
  } catch (err) {
    if (current === request) error.value = extractApiErrorMessage(err, t('mediaTasks.billingReview.failed'))
  } finally {
    if (current === request) busy.value = false
  }
}
watch(() => [props.task.id, props.admin], () => { ++request; busy.value = false; success.value = ''; reset() })
onBeforeUnmount(() => { ++request })
</script>
