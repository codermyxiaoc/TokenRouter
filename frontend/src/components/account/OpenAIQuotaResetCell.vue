<template>
  <div v-if="visible" class="space-y-1">
    <div class="flex flex-wrap items-center gap-1.5">
      <slot name="pre-actions" />

      <!-- 查询和真实重置分开确认，缓存次数只用于展示。 -->
      <button
        type="button"
        data-testid="reset-credit-query"
        class="inline-flex min-w-[54px] items-center justify-center gap-0.5 rounded px-1.5 py-0.5 text-[10px] font-medium text-blue-600 transition-colors hover:bg-blue-50 disabled:cursor-not-allowed disabled:opacity-50 dark:text-blue-400 dark:hover:bg-blue-900/30"
        :disabled="loading || resetting"
        :title="countButtonTitle"
        @click="handleQuery"
      >
        <Icon
          name="refresh"
          size="xs"
          :class="{ 'animate-spin': loading }"
          :stroke-width="2"
        />
        <span>{{ t('admin.accounts.openaiQuotaReset.count') }}</span>
        <span v-if="data">{{ availableResetCount }}</span>
      </button>

      <button
        type="button"
        data-testid="reset-quota"
        class="inline-flex items-center gap-0.5 rounded px-1.5 py-0.5 text-[10px] font-medium text-orange-600 transition-colors hover:bg-orange-50 disabled:cursor-not-allowed disabled:opacity-50 dark:text-orange-400 dark:hover:bg-orange-900/30"
        :disabled="loading || resetting || !canReset"
        :title="resetButtonTitle"
        @click="openResetConfirm"
      >
        <Icon name="refresh" size="xs" :class="{ 'animate-spin': resetting }" :stroke-width="2" />
        {{ t('admin.accounts.openaiQuotaReset.reset') }}
      </button>

      <button
        type="button"
        data-testid="codex-credits"
        class="inline-flex max-w-full items-center gap-1 rounded px-1.5 py-0.5 text-[10px] font-medium text-emerald-700 hover:bg-emerald-50 disabled:opacity-50 dark:text-emerald-400 dark:hover:bg-emerald-900/30"
        :disabled="loading || resetting"
        :title="creditsButtonTitle"
        @click="handleQuery"
      >
        {{ t('admin.accounts.openaiQuotaReset.points') }}
        <span class="truncate tabular-nums">{{ creditsDisplay }}</span>
      </button>
      <OpenAIReferralCell :account="account" />
      <slot />
    </div>

    <div v-if="primaryResetCreditExpiry" class="space-y-1">
      <div class="flex flex-wrap items-center gap-1">
        <span
          class="inline-flex max-w-full items-center rounded bg-gray-100 px-1.5 py-0.5 text-[10px] leading-4 text-gray-600 tabular-nums dark:bg-dark-800 dark:text-gray-300"
          :title="t('admin.accounts.openaiQuotaReset.expiresAtFull', { time: formatResetCreditExpiry(primaryResetCreditExpiry, 'full') })"
        >
          {{ t('admin.accounts.openaiQuotaReset.expiresAt', { time: formatResetCreditExpiry(primaryResetCreditExpiry, 'short') }) }}
        </span>
        <button
          v-if="hiddenResetCreditCount > 0"
          type="button"
          data-testid="reset-credit-expiry-toggle"
          class="inline-flex items-center rounded bg-gray-100 px-1.5 py-0.5 text-[10px] font-medium leading-4 text-gray-600 transition-colors hover:bg-gray-200 dark:bg-dark-800 dark:text-gray-300 dark:hover:bg-dark-700"
          :aria-expanded="showResetCreditDetails"
          :aria-label="resetCreditDetailsToggleLabel"
          :title="resetCreditDetailsTitle"
          @click="toggleResetCreditDetails"
        >
          +{{ hiddenResetCreditCount }}
        </button>
      </div>

      <div
        v-if="showResetCreditDetails && resetCreditExpirations.length > 1"
        data-testid="reset-credit-expiry-details"
        class="inline-grid max-w-full gap-0.5 rounded border border-gray-200 bg-white px-1.5 py-1 text-[10px] leading-4 text-gray-600 shadow-sm dark:border-dark-700 dark:bg-dark-900 dark:text-gray-300"
      >
        <span class="sr-only">{{ t('admin.accounts.openaiQuotaReset.expirationDetails') }}</span>
        <span
          v-for="(expiresAt, index) in resetCreditExpirations"
          :key="`${expiresAt}-${index}`"
          class="flex min-w-0 items-center gap-1 tabular-nums"
          :title="t('admin.accounts.openaiQuotaReset.expiresAtFull', { time: formatResetCreditExpiry(expiresAt, 'full') })"
        >
          <span class="h-1 w-1 shrink-0 rounded-full bg-gray-400 dark:bg-dark-500" />
          <span class="truncate">{{ formatResetCreditExpiry(expiresAt, 'short') }}</span>
        </span>
      </div>
    </div>

    <div
      v-if="error"
      class="max-w-[180px] truncate text-[10px] text-red-600 dark:text-red-400"
      :title="error"
    >
      {{ truncatedError }}
    </div>
    <div
      v-if="resetMessage"
      class="max-w-[220px] text-[10px] text-emerald-600 dark:text-emerald-400"
    >
      {{ resetMessage }}
    </div>
    <div
      v-if="warning"
      class="max-w-[220px] text-[10px] text-amber-600 dark:text-amber-400"
    >
      {{ warning }}
    </div>

    <ConfirmDialog
      :show="showResetConfirm"
      :title="t('admin.accounts.openaiQuotaReset.confirmTitle')"
      :message="t('admin.accounts.openaiQuotaReset.confirmMessage', { count: availableResetCount })"
      :confirm-text="t('admin.accounts.openaiQuotaReset.reset')"
      :cancel-text="t('common.cancel')"
      danger
      @confirm="confirmReset"
      @cancel="showResetConfirm = false"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Account } from '@/types'
import {
  refreshOpenAIQuota,
  resetOpenAIQuota,
  type OpenAIQuotaResetResult,
  type OpenAIQuotaUsage
} from '@/api/admin/accounts'
import OpenAIReferralCell from './OpenAIReferralCell.vue'
import Icon from '@/components/icons/Icon.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'

const props = defineProps<{
  account: Account
}>()

const emit = defineEmits<{
  'quota-reset': [result: OpenAIQuotaResetResult]
}>()

const { t } = useI18n()

const visible = computed(() => props.account.platform === 'openai' && props.account.type === 'oauth')
const loading = ref(false)
const resetting = ref(false)
const hasQueried = ref(false)
const showResetConfirm = ref(false)
const resetMessage = ref<string | null>(null)
const error = ref<string | null>(null)
const warning = ref<string | null>(null)
const data = ref<OpenAIQuotaUsage | null>(null)
const showResetCreditDetails = ref(false)
// 代次同时隔离切账号后再切回、账号类型改变和组件卸载后的异步结果。
let requestGeneration = 0
onBeforeUnmount(() => { requestGeneration++ })

const readCachedCredits = (account: Account) => {
  const snapshot = account.extra?.codex_credits_snapshot
  const credits = snapshot?.credits
  if (!credits || typeof credits.has_credits !== 'boolean' || typeof credits.unlimited !== 'boolean') return null
  if (credits.balance != null && typeof credits.balance !== 'string') return null
  return { credits, fetched_at: snapshot.fetched_at }
}
const creditsData = ref(readCachedCredits(props.account))
const creditsDisplay = computed(() => {
  const credits = creditsData.value?.credits
  if (!credits) return '—'
  if (credits.unlimited) return t('admin.accounts.openaiQuotaReset.pointsUnlimited')
  if (!credits.has_credits) return '0'
  const balance = credits.balance?.trim()
  // 保留上游小数字符串，不把积分误算成重置次数。
  if (balance && Number.isFinite(Number(balance)) && Number(balance) >= 0) return balance
  return t('admin.accounts.openaiQuotaReset.pointsAvailable')
})
const creditsButtonTitle = computed(() => {
  const fetchedAt = creditsData.value?.fetched_at
  const refresh = t('admin.accounts.openaiQuotaReset.pointsTooltip')
  if (!fetchedAt || !Number.isFinite(fetchedAt)) return refresh
  return `${refresh}\n${t('admin.accounts.openaiQuotaReset.pointsUpdatedAt', {
    time: new Date(fetchedAt * 1000).toLocaleString()
  })}`
})

const updateCredits = (usage: OpenAIQuotaUsage | null) => {
  creditsData.value = usage?.credits ? { credits: usage.credits, fetched_at: usage.fetched_at } : null
}

// 只水合仍有有效到期明细的正数快照，避免过期次数继续显示为可用。
const readCachedResetCredits = (account: Account): OpenAIQuotaUsage | null => {
  const cached = account.extra?.codex_reset_credit_snapshot
  if (!cached || typeof cached !== 'object' || Array.isArray(cached)) return null

  const { available_count: count, credits: rawCredits } = cached as {
    available_count?: unknown
    credits?: unknown
  }
  if (typeof count !== 'number' || !Number.isFinite(count)) return null

  const now = Date.now()
  const credits: { expires_at?: string }[] = []
  if (Array.isArray(rawCredits)) {
    for (const credit of rawCredits) {
      if (!credit || typeof credit !== 'object') continue
      const expiresAt = (credit as { expires_at?: unknown }).expires_at
      if (typeof expiresAt !== 'string' || expiresAt.trim() === '') continue
      const expiryTime = new Date(expiresAt).getTime()
      // 无法解析的时间仍保留原文，避免静默少报上游返回的可用次数。
      if (!Number.isNaN(expiryTime) && expiryTime <= now) continue
      credits.push({ expires_at: expiresAt })
    }
  }

  const availableCount = Math.min(Math.max(count, 0), credits.length)
  if (count > 0 && availableCount <= 0) return null
  return {
    fetched_at: 0,
    rate_limit_reset_credits: {
      available_count: availableCount,
      credits
    }
  }
}

data.value = readCachedResetCredits(props.account)

const availableResetCount = computed(() => data.value?.rate_limit_reset_credits?.available_count ?? 0)
const isShadow = computed(() => props.account.parent_account_id != null)
const canReset = computed(() => visible.value && hasQueried.value && availableResetCount.value > 0 && !isShadow.value)
const resetButtonTitle = computed(() => {
  if (isShadow.value) return t('admin.accounts.openaiQuotaReset.resetTooltipShadow')
  if (!hasQueried.value) return t('admin.accounts.openaiQuotaReset.resetTooltipNeedQuery')
  if (!canReset.value) return t('admin.accounts.openaiQuotaReset.resetTooltipNoCredits')
  return t('admin.accounts.openaiQuotaReset.resetTooltipReady')
})
const resetCreditExpirations = computed(() =>
  (data.value?.rate_limit_reset_credits?.credits ?? [])
    .map((credit) => credit.expires_at?.trim() ?? '')
    .filter((expiresAt) => expiresAt.length > 0)
    .sort(compareResetCreditExpiry)
)
const primaryResetCreditExpiry = computed(() => resetCreditExpirations.value[0] ?? '')
const hiddenResetCreditCount = computed(() => Math.max(resetCreditExpirations.value.length - 1, 0))

const countButtonTitle = computed(() => {
  if (!data.value) return t('admin.accounts.openaiQuotaReset.countTooltipLoad')
  return t('admin.accounts.openaiQuotaReset.countTooltipRefresh')
})

const resetCreditDetailsTitle = computed(() =>
  resetCreditExpirations.value
    .map((expiresAt) => formatResetCreditExpiry(expiresAt, 'full'))
    .join('\n')
)

const resetCreditDetailsToggleLabel = computed(() => {
  if (showResetCreditDetails.value) {
    return t('admin.accounts.openaiQuotaReset.collapseExpirations')
  }
  return t('admin.accounts.openaiQuotaReset.expandExpirations', { count: hiddenResetCreditCount.value })
})

const truncatedError = computed(() => {
  if (!error.value) return ''
  return error.value.length > 80 ? `${error.value.slice(0, 80)}...` : error.value
})

const getResetCreditExpiryTime = (value: string): number => {
  const time = new Date(value).getTime()
  return Number.isNaN(time) ? Number.POSITIVE_INFINITY : time
}

function compareResetCreditExpiry(a: string, b: string): number {
  const diff = getResetCreditExpiryTime(a) - getResetCreditExpiryTime(b)
  if (diff !== 0) return diff
  return a.localeCompare(b)
}

const formatResetCreditExpiry = (value: string, style: 'short' | 'full'): string => {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value

  const options: Intl.DateTimeFormatOptions = {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit'
  }
  if (style === 'full') options.year = 'numeric'
  return new Intl.DateTimeFormat(undefined, options).format(date)
}

const extractErrorMessage = (e: unknown): string => {
  const err = e as {
    message?: string
    reason?: string
    response?: { data?: { message?: string; error?: string } }
  }
  return (
    err?.message ||
    err?.reason ||
    err?.response?.data?.message ||
    err?.response?.data?.error ||
    t('common.error')
  )
}

const toggleResetCreditDetails = () => {
  if (hiddenResetCreditCount.value <= 0) return
  showResetCreditDetails.value = !showResetCreditDetails.value
}

const handleQuery = async () => {
  if (!visible.value || loading.value || resetting.value) return
  const accountID = props.account.id
  const generation = ++requestGeneration
  loading.value = true
  hasQueried.value = false
  showResetConfirm.value = false
  error.value = null
  warning.value = null
  resetMessage.value = null
  showResetCreditDetails.value = false
  try {
    const result = await refreshOpenAIQuota(accountID)
    if (generation !== requestGeneration) return
    updateCredits(result)
    data.value = result
    hasQueried.value = true
    if (!result.cache_persisted || result.credits_cache_persisted === false) {
      warning.value = t('admin.accounts.openaiQuotaReset.refreshCachePersistFailed')
    }
  } catch (e) {
    if (generation !== requestGeneration) return
    error.value = extractErrorMessage(e)
  } finally {
    if (generation === requestGeneration) loading.value = false
  }
}

const openResetConfirm = () => {
  if (loading.value || resetting.value || !canReset.value) return
  showResetConfirm.value = true
}

const confirmReset = async () => {
  if (!showResetConfirm.value || loading.value || resetting.value || !canReset.value) return
  showResetConfirm.value = false
  const accountID = props.account.id
  const generation = ++requestGeneration
  resetting.value = true
  hasQueried.value = false
  error.value = null
  warning.value = null
  resetMessage.value = null
  showResetCreditDetails.value = false
  try {
    const result = await resetOpenAIQuota(accountID)
    if (generation !== requestGeneration) return
    // 上游 HTTP 200 仍可能表示没有次数或无需重置，不能将其当作消费成功。
    const code = result.code.trim().toLowerCase()
    if (!['reset', 'success', 'ok'].includes(code) || !(result.windows_reset > 0)) {
      data.value = null
      warning.value = t(`admin.accounts.openaiQuotaReset.${
        code === 'no_credit' ? 'noCreditsAvailable' : 'resetUnknown'
      }`)
      return
    }

    updateCredits(result.quota ?? null)
    // 消费成功但回读失败时丢弃旧次数和到期明细，防止使用旧快照再次消费。
    data.value = result.cache_refreshed ? result.quota ?? null : null
    hasQueried.value = data.value !== null
    resetMessage.value = t('admin.accounts.openaiQuotaReset.resetSuccess', { windows: result.windows_reset })
    const warnings: string[] = []
    if (!result.cache_refreshed || !result.quota || result.warning_code === 'reset_credit_cache_refresh_failed') {
      warnings.push(t('admin.accounts.openaiQuotaReset.resetCacheRefreshFailed'))
    }
    if (!result.account_state_recovered || result.warning_code === 'account_state_recovery_failed') {
      warnings.push(t('admin.accounts.openaiQuotaReset.resetAccountRecoveryFailed'))
    } else if (!result.account || result.warning_code === 'account_state_refresh_failed') {
      warnings.push(t('admin.accounts.openaiQuotaReset.resetAccountRefreshFailed'))
    }
    warning.value = warnings.length ? warnings.join(' ') : null
    emit('quota-reset', result)
  } catch (e) {
    if (generation !== requestGeneration) return
    // 请求断开不代表上游没有消费次数；不自动重试，重新查询后才允许下一次操作。
    data.value = null
    const status = (e as { status?: number; response?: { status?: number } })?.status
      ?? (e as { response?: { status?: number } })?.response?.status
    if (!status || status >= 500) {
      warning.value = t('admin.accounts.openaiQuotaReset.resetUnknown')
    } else {
      error.value = extractErrorMessage(e)
    }
  } finally {
    if (generation === requestGeneration) resetting.value = false
  }
}

watch(
  [() => props.account.id, () => props.account.platform, () => props.account.type, () => props.account.parent_account_id],
  () => {
    requestGeneration++
    data.value = readCachedResetCredits(props.account)
    creditsData.value = readCachedCredits(props.account)
    error.value = null
    warning.value = null
    resetMessage.value = null
    loading.value = false
    resetting.value = false
    hasQueried.value = false
    showResetConfirm.value = false
    showResetCreditDetails.value = false
  },
  { flush: 'sync' }
)

watch(resetCreditExpirations, () => {
  if (hiddenResetCreditCount.value <= 0) {
    showResetCreditDetails.value = false
  }
})
</script>
