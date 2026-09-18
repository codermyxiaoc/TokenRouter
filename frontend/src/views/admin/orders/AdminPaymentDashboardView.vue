<template>
  <AppLayout>
    <template #page-heading-actions>
      <div class="flex items-center justify-end gap-2">
        <DateRangePicker
          v-model:start-date="startDate"
          v-model:end-date="endDate"
          @change="onDateRangeChange"
        />
        <button @click="loadDashboard" :disabled="loading" class="btn btn-secondary h-9 w-9 shrink-0 p-0" :title="t('common.refresh')">
          <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
        </button>
      </div>
    </template>
    <div class="space-y-6">
      <!-- Dashboard Content -->
      <div v-if="loading" class="flex items-center justify-center py-12">
        <LoadingSpinner />
      </div>
      <template v-else-if="displayStats">
        <p class="text-xs leading-relaxed text-gray-500 dark:text-gray-400">
          {{ t('payment.admin.paymentStatisticsHint') }}
        </p>
        <OrderStatsCards :stats="displayStats" />
        <DailyRevenueChart :data="displayStats.daily_series || []" :loading="loading" />
        <PurchaseDistributionChart :items="displayStats.purchase_distribution || []" />
        <div class="grid grid-cols-1 gap-6 lg:grid-cols-2">
          <div class="card p-4">
            <h3 class="mb-4 text-sm font-semibold text-gray-900 dark:text-white">{{ t('payment.admin.paymentDistribution') }}</h3>
            <div v-if="!displayStats.payment_methods?.length" class="flex h-32 items-center justify-center text-sm text-gray-500 dark:text-gray-400">{{ t('payment.admin.noData') }}</div>
            <div v-else class="space-y-3">
              <div v-for="method in displayStats.payment_methods" :key="method.type" class="flex items-center justify-between">
                <div class="flex items-center gap-2">
                  <span :class="['inline-block h-3 w-3 rounded-full', methodColor(method.type)]"></span>
                  <span class="text-sm text-gray-700 dark:text-gray-300">{{ t('payment.methods.' + method.type, method.type) }}</span>
                </div>
                <div class="space-y-1 text-right">
                  <span v-for="[currency, amount] in sortedAmounts(method.amount)" :key="currency" class="block text-sm font-medium text-gray-900 dark:text-white">
                    {{ formatMoney(currency, amount) }}
                  </span>
                  <span class="ml-2 text-xs text-gray-500 dark:text-gray-400">({{ method.count }})</span>
                </div>
              </div>
            </div>
          </div>
          <div class="card p-4">
            <h3 class="mb-4 text-sm font-semibold text-gray-900 dark:text-white">{{ t('payment.admin.topUsers') }}</h3>
            <div v-if="!hasTopUsers(displayStats.top_users)" class="flex h-32 items-center justify-center text-sm text-gray-500 dark:text-gray-400">{{ t('payment.admin.noData') }}</div>
            <div v-else class="space-y-2">
              <div v-for="[currency, users] in sortedTopUsers(displayStats.top_users)" :key="currency" class="space-y-2">
                <p class="text-xs font-semibold text-gray-500 dark:text-gray-400">{{ currency }}</p>
                <div v-for="(user, idx) in users" :key="user.user_id" class="flex items-center justify-between rounded-lg px-3 py-2 hover:bg-gray-50 dark:hover:bg-dark-700">
                  <div class="flex items-center gap-3">
                    <span :class="['flex h-6 w-6 items-center justify-center rounded-full text-xs font-bold', rankClass(idx)]">{{ idx + 1 }}</span>
                    <span class="text-sm text-gray-700 dark:text-gray-300">{{ user.email }}</span>
                  </div>
                  <span class="text-sm font-medium text-gray-900 dark:text-white">{{ formatMoney(currency, user.amount) }}</span>
                </div>
              </div>
            </div>
          </div>
        </div>
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminPaymentAPI } from '@/api/admin/payment'
import { extractI18nErrorMessage } from '@/utils/apiError'
import type { CurrencyAmounts, DashboardStats, TopUserPaymentStats } from '@/types/payment'
import AppLayout from '@/components/layout/AppLayout.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import DateRangePicker from '@/components/common/DateRangePicker.vue'
import Icon from '@/components/icons/Icon.vue'
import OrderStatsCards from '@/components/admin/payment/OrderStatsCards.vue'
import DailyRevenueChart from '@/components/admin/payment/DailyRevenueChart.vue'
import PurchaseDistributionChart from '@/components/admin/payment/PurchaseDistributionChart.vue'
import { normalizePaymentCurrency } from '@/components/payment/currency'

const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(false)
const stats = ref<DashboardStats | null>(null)

// 仅过滤概览的美元金额投影，不删订单数量、不换算币种，也不修改接口原始统计。
// @project-doc docs/domains/payments_and_entitlements.md#payment_dashboard_statistics
const displayStats = computed<DashboardStats | null>(() => {
  if (!stats.value) return null
  const current = stats.value
  return {
    ...current,
    today_amount: visibleAmounts(current.today_amount),
    total_amount: visibleAmounts(current.total_amount),
    avg_amount: visibleAmounts(current.avg_amount),
    daily_series: (current.daily_series || []).map(day => ({ ...day, amount: visibleAmounts(day.amount) })),
    payment_methods: (current.payment_methods || []).map(method => ({ ...method, amount: visibleAmounts(method.amount) })),
    top_users: Object.fromEntries(Object.entries(current.top_users || {})
      .filter(([currency]) => normalizePaymentCurrency(currency) !== 'USD')),
  }
})

function visibleAmounts(amounts: CurrencyAmounts): CurrencyAmounts {
  return Object.fromEntries(Object.entries(amounts || {})
    .filter(([currency]) => normalizePaymentCurrency(currency) !== 'USD'))
}

function formatLocalDate(date: Date): string {
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`
}

function getLast30DaysRange(): { start: string; end: string } {
  const end = new Date()
  const start = new Date()
  start.setDate(start.getDate() - 29)
  return {
    start: formatLocalDate(start),
    end: formatLocalDate(end)
  }
}

const defaultRange = getLast30DaysRange()
const startDate = ref(defaultRange.start)
const endDate = ref(defaultRange.end)

function methodColor(type: string): string {
  const c: Record<string, string> = {
    alipay: 'bg-blue-500', wxpay: 'bg-green-500',
    alipay_direct: 'bg-blue-400', wxpay_direct: 'bg-green-400',
    stripe: 'bg-purple-500', balance: 'bg-amber-500',
  }
  return c[type] || 'bg-gray-400'
}

function rankClass(idx: number): string {
  if (idx === 0) return 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-400'
  if (idx === 1) return 'bg-gray-200 text-gray-600 dark:bg-gray-700 dark:text-gray-300'
  if (idx === 2) return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-400'
  return 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-gray-400'
}

function sortedAmounts(amounts: CurrencyAmounts): [string, number][] {
  return Object.entries(amounts).sort(([left], [right]) => left.localeCompare(right))
}

function sortedTopUsers(usersByCurrency: Record<string, TopUserPaymentStats[]>): [string, TopUserPaymentStats[]][] {
  return Object.entries(usersByCurrency).sort(([left], [right]) => left.localeCompare(right))
}

function hasTopUsers(usersByCurrency: Record<string, TopUserPaymentStats[]>): boolean {
  return Object.values(usersByCurrency).some(users => users.length > 0)
}

function formatMoney(currency: string, amount: number): string {
  return new Intl.NumberFormat(undefined, { style: 'currency', currency }).format(amount)
}

async function loadDashboard() {
  loading.value = true
  try {
    const res = await adminPaymentAPI.getDashboard({
      start_date: startDate.value,
      end_date: endDate.value
    })
    stats.value = res.data
  } catch (err: unknown) {
    appStore.showError(extractI18nErrorMessage(err, t, 'payment.errors', t('common.error')))
  } finally {
    loading.value = false
  }
}

function onDateRangeChange(range: { startDate: string; endDate: string; preset: string | null }) {
  startDate.value = range.startDate
  endDate.value = range.endDate
  void loadDashboard()
}

onMounted(() => loadDashboard())
</script>
