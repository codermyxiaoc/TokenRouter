import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'

import type { DashboardStats } from '@/types'
import DashboardView from '../DashboardView.vue'

// 图表测试只观察传给 Chart.js 的数据与格式化契约，无需浏览器 Canvas。
vi.mock('vue-chartjs', () => ({
  Line: { name: 'Line', props: ['data', 'options'], template: '<div />' }
}))

const { getSnapshotV2, getUserUsageTrend, getUserSpendingRanking } = vi.hoisted(() => ({
  getSnapshotV2: vi.fn(),
  getUserUsageTrend: vi.fn(),
  getUserSpendingRanking: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    dashboard: {
      getSnapshotV2,
      getUserUsageTrend,
      getUserSpendingRanking
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn()
  })
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({
    push: vi.fn()
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

const formatLocalDate = (date: Date): string => {
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

const createDashboardStats = (): DashboardStats => ({
  total_users: 0,
  today_new_users: 0,
  active_users: 0,
  hourly_active_users: 0,
  stats_updated_at: '',
  stats_stale: false,
  total_api_keys: 0,
  active_api_keys: 0,
  total_accounts: 0,
  normal_accounts: 0,
  error_accounts: 0,
  ratelimit_accounts: 0,
  overload_accounts: 0,
  total_requests: 0,
  total_input_tokens: 0,
  total_output_tokens: 0,
  total_cache_creation_tokens: 0,
  total_cache_read_tokens: 0,
  total_tokens: 0,
  total_cost: 0,
  total_actual_cost: 0,
  today_requests: 0,
  today_input_tokens: 0,
  today_output_tokens: 0,
  today_cache_creation_tokens: 0,
  today_cache_read_tokens: 0,
  today_tokens: 0,
  today_cost: 0,
  today_actual_cost: 0,
  average_duration_ms: 0,
  uptime: 0,
  rpm: 0,
  tpm: 0
})

let pinia: ReturnType<typeof createPinia>

describe('admin DashboardView', () => {
  beforeEach(() => {
    pinia = createPinia()
    setActivePinia(pinia)

    getSnapshotV2.mockReset()
    getUserUsageTrend.mockReset()
    getUserSpendingRanking.mockReset()

    getSnapshotV2.mockResolvedValue({
      stats: createDashboardStats(),
      trend: [],
      models: []
    })
    getUserUsageTrend.mockResolvedValue({
      trend: [],
      start_date: '',
      end_date: '',
      granularity: 'hour'
    })
    getUserSpendingRanking.mockResolvedValue({
      ranking: [],
      total_actual_cost: 0,
      total_requests: 0,
      total_tokens: 0,
      start_date: '',
      end_date: ''
    })
  })

  it('uses last 24 hours as default dashboard range', async () => {
    mount(DashboardView, {
      global: {
        plugins: [pinia],
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          LoadingSpinner: true,
          Icon: true,
          DateRangePicker: true,
          Select: true,
          ModelDistributionChart: true,
          TokenUsageTrend: true,
          Line: true
        }
      }
    })

    await flushPromises()

    const now = new Date()
    const yesterday = new Date(now.getTime() - 24 * 60 * 60 * 1000)

    expect(getSnapshotV2).toHaveBeenCalledTimes(1)
    expect(getSnapshotV2).toHaveBeenCalledWith(expect.objectContaining({
      start_date: formatLocalDate(yesterday),
      end_date: formatLocalDate(now),
      granularity: 'hour'
    }))
  })

  it('花费切换重新按实际费用查询 Top 用户并更新曲线与金额单位', async () => {
    getUserUsageTrend.mockImplementation(async () => ({ trend: [
      { date: '2026-09-30', user_id: 9, username: 'owner', tokens: 2000, actual_cost: 1.25 }
    ] }))
    const wrapper = mount(DashboardView, {
      global: { plugins: [pinia], stubs: {
        AppLayout: { template: '<div><slot /></div>' }, LoadingSpinner: true, Icon: true,
        DateRangePicker: true, Select: true, ModelDistributionChart: true, TokenUsageTrend: true
      } }
    })
    await flushPromises()
    expect(getUserUsageTrend).toHaveBeenLastCalledWith(expect.objectContaining({ metric: 'tokens' }))
    expect(wrapper.findComponent({ name: 'Line' }).props('data').datasets[0].data).toEqual([2000])
    await wrapper.get('button[aria-pressed="false"]').trigger('click')
    await flushPromises()
    expect(getUserUsageTrend).toHaveBeenLastCalledWith(expect.objectContaining({ metric: 'actual_cost' }))
    const chart = wrapper.findComponent({ name: 'Line' })
    expect(chart.props('data').datasets[0].data).toEqual([1.25])
    expect(chart.props('options').scales.y.ticks.callback(1.25)).toBe('$1.25')
  })

  it('Token 查询晚到时不会覆盖已切换的实际消费曲线', async () => {
    let resolveTokens!: (value: unknown) => void
    getUserUsageTrend.mockReturnValueOnce(new Promise(resolve => { resolveTokens = resolve }))
    getUserUsageTrend.mockResolvedValueOnce({ trend: [
      { date: '2026-09-30', user_id: 9, username: 'owner', tokens: 2000, actual_cost: 1.25 }
    ] })
    const wrapper = mount(DashboardView, {
      global: { plugins: [pinia], stubs: {
        AppLayout: { template: '<div><slot /></div>' }, LoadingSpinner: true, Icon: true,
        DateRangePicker: true, Select: true, ModelDistributionChart: true, TokenUsageTrend: true
      } }
    })
    await flushPromises()
    await wrapper.get('button[aria-pressed="false"]').trigger('click')
    await flushPromises()
    resolveTokens({ trend: [
      { date: '2026-09-30', user_id: 1, username: 'stale', tokens: 99999, actual_cost: 99 }
    ] })
    await flushPromises()
    const data = wrapper.findComponent({ name: 'Line' }).props('data')
    expect(data.datasets).toHaveLength(1)
    expect(data.datasets[0].label).toBe('owner')
    expect(data.datasets[0].data).toEqual([1.25])
    expect(getUserUsageTrend).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })
})
