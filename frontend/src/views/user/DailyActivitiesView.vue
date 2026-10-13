<template>
  <AppLayout>
    <div class="mx-auto min-w-0 max-w-6xl space-y-6">
      <header class="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 class="text-2xl font-bold text-gray-900 dark:text-white">
            {{ t('dailyActivities.title') }}
          </h1>
          <p class="mt-2 text-sm text-gray-500">
            {{ t('dailyActivities.description') }}
          </p>
        </div>
        <button
          type="button"
          class="btn btn-secondary btn-sm"
          :disabled="loading || !!claiming"
          @click="refresh"
        >
          {{ t('common.refresh') }}
        </button>
      </header>
      <p class="text-xs leading-relaxed text-gray-500">
        {{ t('dailyActivities.eligibilityHint') }}
      </p>
      <p
        v-if="error"
        role="alert"
        class="rounded-xl bg-red-50 p-4 text-sm text-red-600 dark:bg-red-900/20"
      >
        {{ error }}
      </p>
      <p
        v-if="success"
        role="status"
        class="rounded-xl bg-emerald-50 p-4 text-sm text-emerald-700 dark:bg-emerald-900/20 dark:text-emerald-400"
      >
        {{ success }}
      </p>
      <p v-if="loading && !status" class="py-8 text-center text-gray-500">
        {{ t('common.loading') }}
      </p>
      <template v-if="status">
        <p
          v-if="!status.enabled"
          class="rounded-xl bg-gray-100 p-4 text-sm text-gray-600 dark:bg-dark-800 dark:text-gray-300"
        >
          {{ t('dailyActivities.disabled') }}
        </p>
        <p v-if="status.started_at" class="text-xs text-gray-500">
          {{
            t('dailyActivities.startedAt', {
              time: formatTime(status.started_at),
            })
          }}
        </p>
        <!-- 礼包占满首行，两个累计活动在桌面并排，手机自然堆叠。 -->
        <div class="grid min-w-0 gap-5 md:grid-cols-2">
          <section
            v-for="activity in orderedActivities"
            :key="activity.type"
            :data-activity="activity.type"
            class="card min-w-0 border-l-4 p-5 sm:p-7"
            :class="
              activity.type === 'daily_gift'
                ? 'border-l-amber-400 md:col-span-2'
                : 'border-l-emerald-400'
            "
          >
            <div class="flex items-start justify-between gap-4">
              <div>
                <h2 class="text-lg font-bold text-gray-900 dark:text-white">
                  {{ t(`dailyActivities.types.${activity.type}`) }}
                </h2>
                <p class="mt-2 text-sm text-gray-500">
                  {{
                    t(`dailyActivities.rules.${activity.type}`, {
                      amount: money(activity.threshold),
                    })
                  }}
                </p>
                <p class="mt-2 text-sm text-emerald-600 dark:text-emerald-400">
                  {{
                    t('dailyActivities.rewardRange', {
                      min: money(0.1),
                      max: money(activity.reward_max),
                    })
                  }}
                </p>
              </div>
              <Icon
                :name="
                  activity.type === 'daily_gift'
                    ? 'gift'
                    : activity.type === 'consumption'
                      ? 'chart'
                      : 'creditCard'
                "
                size="lg"
                class="shrink-0 text-primary-500"
              />
            </div>
            <div
              class="mt-7 flex flex-wrap items-baseline justify-between gap-2"
            >
              <span class="text-sm text-gray-500">{{
                t(
                  activity.type === 'daily_gift'
                    ? 'dailyActivities.todayRecharge'
                    : 'dailyActivities.totalProgress',
                )
              }}</span
              ><strong
                class="break-all text-xl tabular-nums text-gray-900 dark:text-white"
                >{{ money(activity.eligible_amount) }} /
                {{ money(activity.threshold) }}</strong
              >
            </div>
            <div
              class="mt-3 h-2.5 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700"
              role="progressbar"
              :aria-label="t(`dailyActivities.types.${activity.type}`)"
              :aria-valuenow="percent(activity.progress)"
              :aria-valuemin="0"
              :aria-valuemax="100"
            >
              <div
                class="h-full rounded-full bg-emerald-500"
                :style="{ width: `${percent(activity.progress)}%` }"
              />
            </div>
            <div class="mt-4 flex flex-wrap justify-between gap-2 text-sm">
              <span
                :class="
                  activity.available_count > 0
                    ? 'text-emerald-600 dark:text-emerald-400'
                    : 'text-gray-500'
                "
                >{{
                  t('dailyActivities.available', {
                    count: activity.available_count,
                  })
                }}</span
              ><span class="text-gray-500">{{
                t('dailyActivities.claimed', { count: activity.claimed_count })
              }}</span>
            </div>
            <p class="mt-5 text-xs text-gray-500">
              {{
                t(
                  activity.type === 'daily_gift'
                    ? 'dailyActivities.dailyReset'
                    : 'dailyActivities.persistent',
                )
              }}
            </p>
            <p
              v-if="
                activity.type !== 'daily_gift' && activity.available_count > 100
              "
              class="mt-2 text-xs text-gray-500"
            >
              {{ t('dailyActivities.batchLimit') }}
            </p>
            <p
              v-if="pending[activity.type]"
              class="mt-3 text-xs text-amber-600"
              :data-pending="activity.type"
            >
              {{ t('dailyActivities.pendingHint') }}
            </p>
            <div class="mt-5 flex flex-wrap gap-3">
              <RouterLink
                v-if="activity.type !== 'consumption' && paymentEnabled"
                to="/purchase"
                class="btn btn-secondary flex-1"
                >{{ t('dailyActivities.goRecharge') }}</RouterLink
              >
              <button
                type="button"
                class="btn btn-primary flex-1"
                :data-claim="activity.type"
                :disabled="cannotClaim(activity)"
                @click="claim(activity.type, false)"
              >
                {{
                  claiming === activity.type
                    ? t('common.processing')
                    : pending[activity.type]
                      ? t('dailyActivities.retryClaim')
                      : t(
                          activity.type === 'daily_gift'
                            ? 'dailyActivities.claimGift'
                            : 'dailyActivities.claimOne',
                        )
                }}
              </button>
              <button
                v-if="activity.type !== 'daily_gift'"
                type="button"
                class="btn btn-secondary flex-1"
                :data-claim-all="activity.type"
                :disabled="cannotClaim(activity) || !!pending[activity.type]"
                @click="claim(activity.type, true)"
              >
                {{ t('dailyActivities.claimAll') }}
              </button>
            </div>
          </section>
        </div>
      </template>
      <section class="card min-w-0 overflow-hidden">
        <div
          class="flex flex-wrap items-center justify-between gap-4 p-5 sm:p-7"
        >
          <div>
            <h2 class="text-lg font-bold text-gray-900 dark:text-white">
              {{ t('dailyActivities.history') }}
            </h2>
            <p class="mt-2 text-sm text-gray-500">
              {{ t('dailyActivities.historyHint') }}
            </p>
          </div>
          <div class="w-full sm:w-48">
            <Select
              :model-value="historyType"
              :options="filterOptions"
              :aria-label="t('dailyActivities.filter')"
              @update:model-value="changeFilter"
            />
          </div>
        </div>
        <p
          v-if="historyError"
          role="alert"
          class="px-5 pb-5 text-sm text-red-600"
        >
          {{ historyError }}
        </p>
        <div :aria-busy="historyLoading" class="overflow-x-auto">
          <table class="w-full text-left text-sm">
            <thead
              class="border-y border-gray-100 text-gray-500 dark:border-dark-700"
            >
              <tr>
                <th class="px-5 py-3 font-medium sm:px-7">
                  {{ t('dailyActivities.time') }}
                </th>
                <th class="px-4 py-3 font-medium">
                  {{ t('dailyActivities.activity') }}
                </th>
                <th class="px-5 py-3 text-right font-medium sm:px-7">
                  {{ t('dailyActivities.reward') }}
                </th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr v-for="item in history.items" :key="item.id">
                <td class="whitespace-nowrap px-5 py-4 text-gray-500 sm:px-7">
                  {{ formatTime(item.created_at) }}
                </td>
                <td class="whitespace-nowrap px-4 py-4">
                  {{ t(`dailyActivities.types.${item.activity_type}`) }}
                </td>
                <td
                  class="whitespace-nowrap px-5 py-4 text-right font-semibold tabular-nums text-emerald-600 dark:text-emerald-400 sm:px-7"
                >
                  +{{ money(item.amount) }}
                </td>
              </tr>
              <tr v-if="!history.items.length">
                <td colspan="3" class="px-5 py-10 text-center text-gray-500">
                  {{
                    t(
                      historyLoading
                        ? 'common.loading'
                        : 'dailyActivities.empty',
                    )
                  }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <Pagination
          :page="historyPage"
          :page-size="20"
          :total="history.total"
          :show-page-size-selector="false"
          @update:page="changePage"
        />
      </section>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import Select from '@/components/common/Select.vue'
import Pagination from '@/components/common/Pagination.vue'
import { useAuthStore } from '@/stores/auth'
import { useAppStore } from '@/stores/app'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'
import { extractApiErrorMessage } from '@/utils/apiError'
import {
  clearPendingClaim,
  createClaimKey,
  readPendingClaim,
  storePendingClaim,
} from '@/utils/dailyActivityClaims'
import {
  dailyActivitiesAPI,
  dailyActivityTypes,
  type DailyActivityClaimInput,
  type DailyActivitiesStatus,
  type DailyActivityHistory,
  type DailyActivityType,
  type DailyActivityStatus,
} from '@/api/dailyActivities'

const { t, locale } = useI18n()
const auth = useAuthStore()
const app = useAppStore()
const { formatBalanceAmount } = useBalanceDisplay()
const money = (value: number) =>
  formatBalanceAmount(value, { fractionDigits: 2 })
const formatTime = (value: string) =>
  new Date(value).toLocaleString(locale.value, {
    timeZone: 'Asia/Shanghai',
    hour12: false,
  })
const status = ref<DailyActivitiesStatus>()
const loading = ref(false)
const error = ref('')
const success = ref('')
const claiming = ref<DailyActivityType | ''>('')
const pending = reactive<
  Partial<Record<DailyActivityType, DailyActivityClaimInput>>
>({})
const history = ref<DailyActivityHistory>({
  items: [],
  total: 0,
  page: 1,
  page_size: 20,
})
const historyPage = ref(1)
const historyType = ref<DailyActivityType | ''>('')
const historyLoading = ref(false)
const historyError = ref('')
let statusController: AbortController | undefined
let historyController: AbortController | undefined
let disposed = false
const paymentEnabled = computed(
  () => app.cachedPublicSettings?.payment_enabled === true,
)
const orderedActivities = computed(() =>
  dailyActivityTypes.flatMap(
    (type) =>
      status.value?.activities.filter((item) => item.type === type) || [],
  ),
)
const filterOptions = computed(() => [
  { value: '', label: t('dailyActivities.allTypes') },
  ...dailyActivityTypes.map((type) => ({
    value: type,
    label: t(`dailyActivities.types.${type}`),
  })),
])
const percent = (value: number) =>
  Math.max(0, Math.min(100, Number.isFinite(value) ? value * 100 : 0))
function cannotClaim(activity: DailyActivityStatus) {
  return (
    !!claiming.value ||
    loading.value ||
    (!pending[activity.type] &&
      (!status.value?.enabled || activity.available_count < 1))
  )
}
async function loadStatus() {
  statusController?.abort()
  const controller = new AbortController()
  statusController = controller
  loading.value = true
  try {
    const result = await dailyActivitiesAPI.status(controller.signal)
    if (!controller.signal.aborted) status.value = result
  } catch (err) {
    if (!controller.signal.aborted) {
      status.value = undefined
      error.value = extractApiErrorMessage(err, t('dailyActivities.loadFailed'))
    }
  } finally {
    if (!controller.signal.aborted) loading.value = false
  }
}
async function loadHistory() {
  historyController?.abort()
  const controller = new AbortController()
  historyController = controller
  historyLoading.value = true
  historyError.value = ''
  try {
    const result = await dailyActivitiesAPI.history(
      historyPage.value,
      historyType.value,
      controller.signal,
    )
    if (!controller.signal.aborted) history.value = result
  } catch (err) {
    if (!controller.signal.aborted) {
      history.value = {
        items: [],
        total: 0,
        page: historyPage.value,
        page_size: 20,
      }
      historyError.value = extractApiErrorMessage(
        err,
        t('dailyActivities.loadFailed'),
      )
    }
  } finally {
    if (!controller.signal.aborted) historyLoading.value = false
  }
}
function changeFilter(value: string | number | boolean | null) {
  historyType.value = dailyActivityTypes.includes(value as DailyActivityType)
    ? (value as DailyActivityType)
    : ''
  historyPage.value = 1
  void loadHistory()
}
function changePage(page: number) {
  historyPage.value = page
  void loadHistory()
}
async function refresh() {
  error.value = ''
  await Promise.all([loadStatus(), loadHistory()])
}
async function claim(type: DailyActivityType, all: boolean) {
  if (claiming.value || !auth.user?.id) return
  const userId = auth.user.id
  claiming.value = type
  error.value = ''
  success.value = ''
  try {
    const input = pending[type] || {
      activity_type: type,
      all,
      idempotency_key: createClaimKey(),
    }
    pending[type] = input
    if (!storePendingClaim(userId, input)) {
      throw new Error(t('dailyActivities.storageUnavailable'))
    }
    const result = await dailyActivitiesAPI.claim(input)
    // 确认响应后才能清除请求号；后续刷新失败不能把已入账结果当成失败重发。
    clearPendingClaim(userId, type)
    delete pending[type]
    if (disposed) return
    success.value = t('dailyActivities.claimSuccess', {
      amount: money(result.amount),
      count: result.items.length,
    })
    historyPage.value = 1
    await Promise.allSettled([loadStatus(), loadHistory(), auth.refreshUser()])
  } catch (err) {
    // 后续权限错误也不能证明首次请求未入账；只有成功回放才能清除请求号。
    if (!disposed)
      error.value = extractApiErrorMessage(
        err,
        t('dailyActivities.claimFailed'),
      )
  } finally {
    claiming.value = ''
  }
}
onMounted(() => {
  if (auth.user?.id)
    for (const type of dailyActivityTypes) {
      const input = readPendingClaim(auth.user.id, type)
      if (input) pending[type] = input
    }
  void refresh()
})
onBeforeUnmount(() => {
  disposed = true
  statusController?.abort()
  historyController?.abort()
})
</script>
