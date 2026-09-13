<template>
  <div class="grid grid-cols-1 gap-6 lg:grid-cols-2">
    <div class="card p-4">
      <h3 class="mb-4 text-sm font-semibold text-gray-900 dark:text-white">
        {{ t('usage.teamMemberTrend') }}
      </h3>
      <div v-if="loading" class="flex h-48 items-center justify-center">
        <LoadingSpinner />
      </div>
      <div v-else-if="lineData" class="h-48">
        <Line :data="lineData" :options="lineOptions" />
      </div>
      <div v-else class="flex h-48 items-center justify-center text-sm text-gray-500 dark:text-gray-400">
        {{ t('admin.dashboard.noDataAvailable') }}
      </div>
    </div>

    <div class="card p-4">
      <h3 class="mb-4 text-sm font-semibold text-gray-900 dark:text-white">
        {{ t('usage.teamMemberComparison') }}
      </h3>
      <div v-if="loading" class="flex h-48 items-center justify-center">
        <LoadingSpinner />
      </div>
      <div v-else-if="comparisonData" class="h-48">
        <Bar :data="comparisonData" :options="comparisonOptions" />
      </div>
      <div v-else class="flex h-48 items-center justify-center text-sm text-gray-500 dark:text-gray-400">
        {{ t('admin.dashboard.noDataAvailable') }}
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  BarElement,
  CategoryScale,
  Chart as ChartJS,
  Legend,
  LinearScale,
  LineElement,
  PointElement,
  Tooltip,
} from 'chart.js'
import { Bar, Line } from 'vue-chartjs'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'
import { useTheme } from '@/composables/useTheme'
import { externalTooltipHandler, hideExternalTooltip } from '@/utils/chartExternalTooltip'
import type { TeamUsageSummary } from '@/api/team'

ChartJS.register(BarElement, CategoryScale, Legend, LinearScale, LineElement, PointElement, Tooltip)

onBeforeUnmount(hideExternalTooltip)

interface MemberUsageSeries {
  userID: number
  label: string
  summary: TeamUsageSummary
}

const props = defineProps<{
  series: MemberUsageSeries[]
  loading?: boolean
}>()

const { t } = useI18n()
const { formatBalanceAmount } = useBalanceDisplay()
const { isDark } = useTheme()

// 颜色按成员稳定分配，避免日期刷新后折线颜色跳变。
const palette = ['#2563eb', '#059669', '#d97706', '#dc2626', '#7c3aed', '#0891b2', '#db2777', '#4d7c0f', '#475569', '#ea580c']
const textColor = computed(() => isDark.value ? '#e5e7eb' : '#374151')
const gridColor = computed(() => isDark.value ? '#3f3f46' : '#e5e7eb')
const labels = computed(() => Array.from(new Set(props.series.flatMap((item) => item.summary.daily.map((point) => point.date)))).sort())
const seriesWithUsage = computed(() => props.series.filter((item) => item.summary.request_count > 0 || item.summary.actual_cost > 0))

const lineData = computed(() => {
  if (labels.value.length === 0 || seriesWithUsage.value.length === 0) return null
  return {
    labels: labels.value,
    datasets: seriesWithUsage.value.map((item, index) => {
      const daily = new Map(item.summary.daily.map((point) => [point.date, point.actual_cost]))
      const color = palette[index % palette.length]
      return {
        label: item.label,
        data: labels.value.map((date) => daily.get(date) ?? 0),
        borderColor: color,
        backgroundColor: `${color}20`,
        pointRadius: 2,
        tension: 0.3,
      }
    }),
  }
})

const comparisonData = computed(() => {
  if (seriesWithUsage.value.length === 0) return null
  return {
    labels: seriesWithUsage.value.map((item) => item.label),
    datasets: [{
      data: seriesWithUsage.value.map((item) => item.summary.actual_cost),
      backgroundColor: seriesWithUsage.value.map((_, index) => palette[index % palette.length]),
      borderWidth: 0,
    }],
  }
})

const legend = computed(() => ({
  position: 'top' as const,
  labels: { color: textColor.value, usePointStyle: true, pointStyle: 'circle' as const, padding: 14, font: { size: 11 } },
}))

const lineOptions = computed(() => ({
  responsive: true,
  maintainAspectRatio: false,
  interaction: { intersect: false, mode: 'index' as const },
  plugins: {
    legend: legend.value,
    tooltip: {
      enabled: false,
      external: externalTooltipHandler,
      callbacks: { label: (context: any) => `${context.dataset.label}: ${formatBalanceAmount(Number(context.raw), { fractionDigits: 4 })}` }
    },
  },
  scales: {
    x: { grid: { color: gridColor.value }, ticks: { color: textColor.value, font: { size: 10 } } },
    y: { beginAtZero: true, grid: { color: gridColor.value }, ticks: { color: textColor.value, callback: (value: string | number) => formatBalanceAmount(Number(value), { fractionDigits: 2 }) } },
  },
}))

const comparisonOptions = computed(() => ({
  responsive: true,
  maintainAspectRatio: false,
  indexAxis: 'y' as const,
  plugins: {
    legend: { display: false },
    tooltip: {
      enabled: false,
      external: externalTooltipHandler,
      callbacks: { label: (context: any) => `${context.label}: ${formatBalanceAmount(Number(context.raw), { fractionDigits: 4 })}` }
    },
  },
  scales: {
    x: { beginAtZero: true, grid: { color: gridColor.value }, ticks: { color: textColor.value, callback: (value: string | number) => formatBalanceAmount(Number(value), { fractionDigits: 2 }) } },
    y: { grid: { display: false }, ticks: { color: textColor.value, font: { size: 11 } } },
  },
}))
</script>
