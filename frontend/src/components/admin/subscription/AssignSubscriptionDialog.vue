<template>
  <BaseDialog
    :show="show"
    :title="t('admin.subscriptions.assignSubscription')"
    width="normal"
    :close-on-escape="!submitting"
    @close="closeDialog"
  >
    <form id="assign-subscription-form" class="space-y-5" @submit.prevent="submitAssignment">
      <div>
        <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
          <input v-model="batchEnabled" type="checkbox" class="checkbox" :disabled="formLocked" @change="resetUsers" />
          {{ t('admin.subscriptions.batchAssign.enable') }}
        </label>
        <p v-if="batchEnabled" class="input-hint">{{ t('admin.subscriptions.batchAssign.hint') }}</p>
      </div>
      <div>
        <label for="subscription-assign-user" class="input-label">{{ t('admin.subscriptions.form.user') }}</label>
        <div ref="searchContainer" class="relative" data-assign-user-search>
          <input
            id="subscription-assign-user"
            v-model="searchKeyword"
            type="text"
            class="input pr-8"
            :placeholder="t('admin.usage.searchUserPlaceholder')"
            :disabled="formLocked || (batchEnabled && selectedUsers.length >= 100)"
            @input="queueUserSearch"
            @focus="showUserDropdown = true"
          />
          <button
            v-if="selectedUser"
            type="button"
            class="absolute right-2 top-1/2 -translate-y-1/2 text-gray-400 hover:text-gray-600 dark:hover:text-gray-300"
            :disabled="formLocked"
            :aria-label="t('admin.subscriptions.batchAssign.removeUser', { email: selectedUser.email })"
            @click="resetUsers"
          ><Icon name="x" size="sm" /></button>
          <div
            v-if="showUserDropdown && !formLocked && (searchResults.length > 0 || searchKeyword)"
            class="absolute z-50 mt-1 max-h-60 w-full overflow-auto rounded-lg border border-gray-200 bg-white shadow-lg dark:border-gray-700 dark:bg-gray-800"
          >
            <div v-if="searchLoading" class="px-4 py-3 text-sm text-gray-500 dark:text-gray-400">{{ t('common.loading') }}</div>
            <div v-else-if="searchResults.length === 0" class="px-4 py-3 text-sm text-gray-500 dark:text-gray-400">{{ t('common.noOptionsFound') }}</div>
            <template v-else>
              <button
                v-for="user in searchResults"
                :key="user.id"
                type="button"
                class="w-full px-4 py-2 text-left text-sm hover:bg-gray-100 disabled:cursor-not-allowed disabled:opacity-50 dark:hover:bg-gray-700"
                :disabled="batchEnabled && selectedUsers.some(selected => selected.id === user.id)"
                :data-user-id="user.id"
                @click="selectUser(user)"
              >
                <span class="font-medium text-gray-900 dark:text-white">{{ user.email }}</span>
                <span class="ml-2 text-gray-500 dark:text-gray-400">#{{ user.id }}</span>
              </button>
            </template>
          </div>
        </div>
        <div v-if="batchEnabled && selectedUsers.length > 0" class="mt-2 space-y-2" data-test="assign-users">
          <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.subscriptions.batchAssign.selected', { count: selectedUsers.length }) }}</p>
          <ul class="flex max-h-40 flex-wrap gap-2 overflow-y-auto">
            <li v-for="user in selectedUsers" :key="user.id" class="flex max-w-full items-center gap-1 rounded-lg bg-gray-100 px-2 py-1 text-sm dark:bg-dark-700">
              <span class="truncate" :title="user.email">{{ user.email }}</span>
              <button
                type="button"
                class="shrink-0 rounded p-0.5 text-gray-400 hover:text-red-500 disabled:opacity-50"
                :disabled="formLocked"
                :aria-label="t('admin.subscriptions.batchAssign.removeUser', { email: user.email })"
                @click="removeUser(user.id)"
              ><Icon name="x" size="sm" /></button>
            </li>
          </ul>
        </div>
      </div>
      <div>
        <label class="input-label">{{ t('payment.admin.planName') }}</label>
        <Select
          v-model="form.plan_id"
          :options="planOptions"
          :disabled="formLocked"
          :placeholder="t('admin.announcements.form.selectPackages')"
        />
        <p class="input-hint">{{ t('admin.announcements.form.selectPackages') }}</p>
      </div>
      <div>
        <label for="subscription-assign-days" class="input-label">{{ t('admin.subscriptions.form.validityDays') }}</label>
        <input id="subscription-assign-days" v-model.number="form.validity_days" type="number" min="1" max="36500" step="1" class="input" :disabled="formLocked" />
        <p class="input-hint">{{ t('admin.subscriptions.validityHint') }}</p>
      </div>
      <p v-if="pendingAttempt && !submitting" role="alert" class="rounded-lg bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-300" data-test="assign-pending">
        {{ t(pendingAttempt.blocked ? 'admin.subscriptions.batchAssign.blockedHint' : 'admin.subscriptions.batchAssign.pendingHint') }}
      </p>
      <div v-if="batchResult" class="space-y-2 text-sm" role="status" data-test="batch-assign-result">
        <p>{{ t('admin.subscriptions.batchAssign.result', { success: batchResult.success_count, failed: batchResult.failed_count }) }}</p>
        <ul v-if="batchResult.errors.length" class="max-h-40 space-y-1 overflow-y-auto text-red-600 dark:text-red-400">
          <li v-for="(error, index) in batchResult.errors" :key="index">{{ error }}</li>
        </ul>
        <p v-if="batchResult.failed_count > 0 && !pendingAttempt" class="input-hint">{{ t('admin.subscriptions.batchAssign.retryHint') }}</p>
      </div>
    </form>
    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" :disabled="submitting" @click="closeDialog">
          {{ batchResult || pendingAttempt ? t('common.close') : t('common.cancel') }}
        </button>
        <button
          type="submit"
          form="assign-subscription-form"
          class="btn btn-primary"
          :disabled="submitting || (batchEnabled && selectedUsers.length === 0)"
        >
          {{ submitting ? t('admin.subscriptions.assigning') : pendingAttempt ? t(pendingAttempt.blocked ? 'admin.subscriptions.batchAssign.confirmResult' : 'admin.subscriptions.batchAssign.retry') : t('admin.subscriptions.assign') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { BulkAssignSubscriptionResult } from '@/api/admin/subscriptions'
import type { AdminUser, BulkAssignSubscriptionRequest } from '@/types'
import type { SubscriptionPlan } from '@/types/payment'
import { useAppStore } from '@/stores/app'
import { readPendingSubscriptionOperation, writePendingSubscriptionOperation } from '@/utils/subscriptionOperationStorage'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'

const props = defineProps<{ show: boolean; plans: SubscriptionPlan[] }>()
const emit = defineEmits<{ close: []; assigned: [] }>()
const { t } = useI18n()
const appStore = useAppStore()
const batchEnabled = ref(false)
const selectedUser = ref<AdminUser | null>(null)
const selectedUsers = ref<AdminUser[]>([])
const submitting = ref(false)
const batchResult = ref<BulkAssignSubscriptionResult | null>(null)
interface PendingAssignment {
  payload: BulkAssignSubscriptionRequest
  key: string
  unknown: boolean
  blocked: boolean
}
const pendingAttempt = ref<PendingAssignment | null>(null)
const form = reactive({ plan_id: null as number | null, validity_days: 30 })
const formLocked = computed(() => submitting.value || pendingAttempt.value !== null)
const planOptions = computed(() => props.plans.map(plan => ({ value: plan.id, label: plan.name, description: plan.description })))
const searchContainer = ref<HTMLElement | null>(null)
const searchKeyword = ref('')
const searchResults = ref<AdminUser[]>([])
const searchLoading = ref(false)
const showUserDropdown = ref(false)
let searchTimer: ReturnType<typeof setTimeout> | undefined
let searchGeneration = 0

const invalidateSearch = () => {
  if (searchTimer) clearTimeout(searchTimer)
  searchGeneration++
  searchLoading.value = false
  searchResults.value = []
}

const resetUsers = () => {
  if (formLocked.value) return
  invalidateSearch()
  selectedUser.value = null
  selectedUsers.value = []
  searchKeyword.value = ''
  showUserDropdown.value = false
  batchResult.value = null
}

const queueUserSearch = () => {
  // 输入一变就撤销单用户选择，并让在途旧查询失效，避免误给旧目标发放。
  if (selectedUser.value && searchKeyword.value.trim() !== selectedUser.value.email) selectedUser.value = null
  invalidateSearch()
  const keyword = searchKeyword.value.trim()
  if (!keyword) return
  const generation = searchGeneration
  searchLoading.value = true
  showUserDropdown.value = true
  searchTimer = setTimeout(async () => {
    try {
      const result = await adminAPI.users.list(1, 30, { search: keyword, sort_by: 'email', sort_order: 'asc' })
      if (generation === searchGeneration && props.show) searchResults.value = result.items
    } catch {
      if (generation === searchGeneration) searchResults.value = []
    } finally {
      if (generation === searchGeneration) searchLoading.value = false
    }
  }, 300)
}

const selectUser = (user: AdminUser) => {
  if (formLocked.value) return
  if (batchEnabled.value) {
    if (selectedUsers.value.length >= 100 || selectedUsers.value.some(selected => selected.id === user.id)) return
    selectedUsers.value = [...selectedUsers.value, user]
    searchKeyword.value = ''
  } else {
    selectedUser.value = user
    searchKeyword.value = user.email
  }
  invalidateSearch()
  showUserDropdown.value = false
}

const removeUser = (id: number) => {
  if (!formLocked.value) selectedUsers.value = selectedUsers.value.filter(user => user.id !== id)
}

const closeDialog = () => {
  if (submitting.value) return
  // 结果未确认时保留参数与幂等键，关闭再打开仍只能确认同一次分配。
  if (!pendingAttempt.value) {
    resetUsers()
    batchEnabled.value = false
    form.plan_id = null
    form.validity_days = 30
  }
  emit('close')
}

const submitAssignment = async () => {
  if (submitting.value) return
  if (!pendingAttempt.value) {
    if (batchEnabled.value ? selectedUsers.value.length === 0 : !selectedUser.value) {
      appStore.showError(t('admin.subscriptions.pleaseSelectUser'))
      return
    }
    if (!form.plan_id) {
      appStore.showError(t('admin.announcements.form.selectPackages'))
      return
    }
    if (!Number.isInteger(form.validity_days) || form.validity_days < 1 || form.validity_days > 36500) {
      appStore.showError(t('admin.subscriptions.validityDaysRequired'))
      return
    }
  }

  submitting.value = true
  showUserDropdown.value = false
  try {
    if (batchEnabled.value) {
      // 明确收到部分成功结果后才换键；网络异常不能把同一批用户重新发放一遍。
      pendingAttempt.value ??= {
        payload: { user_ids: selectedUsers.value.map(user => user.id), plan_id: form.plan_id!, validity_days: form.validity_days },
        key: `subscription-assign-${globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`}`,
        unknown: false,
        blocked: false
      }
      const result = await adminAPI.subscriptions.bulkAssign(pendingAttempt.value.payload, pendingAttempt.value.key)
      // 不完整响应视为未知结果，必须保留原键确认，不能误把成功用户留在待重试名单。
      const requested = new Set(pendingAttempt.value.payload.user_ids)
      // 新的带幂等键接口返回紧凑状态，旧接口仍可能返回完整订阅，兼容两种形状。
      const states = result?.statuses
      const hasStatuses = states !== undefined && states !== null
      const returnedUsers = hasStatuses
        ? Array.from(requested).filter(id => states[String(id)] === 'active' || states[String(id)] === 'queued')
        : Array.isArray(result?.subscriptions) ? result.subscriptions.map(subscription => subscription.user_id) : []
      if (!Array.isArray(result?.subscriptions) || !Array.isArray(result?.errors)
        || !Number.isInteger(result.success_count) || !Number.isInteger(result.failed_count)
        || result.success_count < 0 || result.failed_count < 0
        || result.success_count + result.failed_count !== requested.size
        || returnedUsers.length !== result.success_count || new Set(returnedUsers).size !== result.success_count
        || returnedUsers.some(id => !requested.has(id))
        || (hasStatuses && (Object.keys(states).length !== requested.size
          || Array.from(requested).some(id => !['active', 'queued', 'failed'].includes(states[String(id)]))))) {
        throw new Error('Invalid subscription assignment result')
      }
      batchResult.value = result
      const succeeded = new Set(returnedUsers)
      selectedUsers.value = selectedUsers.value.filter(user => !succeeded.has(user.id))
      pendingAttempt.value = null
      if (result.success_count > 0) {
        appStore.showSuccess(t('admin.subscriptions.batchAssign.result', { success: result.success_count, failed: result.failed_count }))
        emit('assigned')
      }
      return
    }
    await adminAPI.subscriptions.assign({ user_id: selectedUser.value!.id, plan_id: form.plan_id!, validity_days: form.validity_days })
    appStore.showSuccess(t('admin.subscriptions.subscriptionAssigned'))
    emit('assigned')
    submitting.value = false
    closeDialog()
  } catch (error: unknown) {
    // API 客户端已归一化异常到顶层；兼容测试及旧调用仍携带的 Axios response。
    const failure = error as {
      status?: number; reason?: string; message?: string
      response?: { status?: number; data?: { reason?: string; detail?: string; message?: string } }
    }
    const status = failure.status ?? failure.response?.status
    const reason = failure.reason ?? failure.response?.data?.reason
    const message = failure.response?.data?.detail || failure.response?.data?.message || failure.message
    if (pendingAttempt.value) {
      // 仅首次、确认执行前的校验拒绝可以编辑；后续 400 不能抹掉之前的未知执行结果。
      const rejectedBeforeExecution = status === 400 && (
        reason === 'IDEMPOTENCY_KEY_INVALID' || reason === 'IDEMPOTENCY_PAYLOAD_INVALID'
        || (!reason && message?.startsWith('Invalid request:'))
      )
      if (rejectedBeforeExecution && !pendingAttempt.value.unknown) {
        pendingAttempt.value = null
      } else {
        pendingAttempt.value.unknown = true
        if (reason === 'IDEMPOTENCY_RESULT_UNCONFIRMED') pendingAttempt.value.blocked = true
      }
    }
    appStore.showError(message || t('admin.subscriptions.failedToAssign'))
  } finally {
    submitting.value = false
  }
}

const handleClickOutside = (event: MouseEvent) => {
  if (event.target instanceof Node && !searchContainer.value?.contains(event.target)) showUserDropdown.value = false
}

const restorePendingAssignment = () => {
  const saved = readPendingSubscriptionOperation<{ attempt?: PendingAssignment; users?: Array<{ id: number; email: string }> }>('assign')
  const attempt = saved?.attempt
  const payload = attempt?.payload
  const ids = payload?.user_ids
  if (!attempt || typeof attempt.key !== 'string' || attempt.key.length === 0 || attempt.key.length > 256
    || !payload || !Number.isSafeInteger(payload.plan_id) || payload.plan_id <= 0
    || !Number.isInteger(payload.validity_days) || payload.validity_days! < 1 || payload.validity_days! > 36500
    || !Array.isArray(ids) || ids.length === 0 || ids.length > 100 || new Set(ids).size !== ids.length
    || ids.some(id => !Number.isSafeInteger(id) || id <= 0)
    || !Array.isArray(saved.users) || saved.users.length !== ids.length
    || ids.some(id => !saved.users!.some(user => user.id === id && typeof user.email === 'string'))
    || typeof attempt.blocked !== 'boolean') return
  // 页面重新载入代表可能打断了在途请求，恢复后始终按未知结果处理，不能因后续 400 解除锁定。
  batchEnabled.value = true
  form.plan_id = payload.plan_id
  form.validity_days = payload.validity_days!
  selectedUsers.value = saved.users as AdminUser[]
  pendingAttempt.value = { payload, key: attempt.key, unknown: true, blocked: attempt.blocked }
}

restorePendingAssignment()
watch(pendingAttempt, attempt => {
  writePendingSubscriptionOperation('assign', attempt ? {
    attempt,
    users: selectedUsers.value.map(user => ({ id: user.id, email: user.email }))
  } : null)
}, { deep: true, flush: 'sync' })

watch(() => props.show, show => {
  if (!show) {
    invalidateSearch()
    showUserDropdown.value = false
  }
})
onMounted(() => document.addEventListener('click', handleClickOutside))
onUnmounted(() => {
  invalidateSearch()
  document.removeEventListener('click', handleClickOutside)
})
</script>
