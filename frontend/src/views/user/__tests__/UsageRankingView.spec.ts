import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import UsageRankingView from '../UsageRankingView.vue'

const { getRanking } = vi.hoisted(() => ({
  getRanking: vi.fn(),
}))

vi.mock('@/api/usage', () => ({
  usageAPI: { getRanking },
}))

vi.mock('@/composables/useBalanceDisplay', () => ({
  useBalanceDisplay: () => ({
    balanceUnitName: { value: 'USD' },
    formatBalanceAmount: (value: number, options?: { fractionDigits?: number }) =>
      `$${value.toFixed(options?.fractionDigits ?? 2)}`,
  }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, string | number>) => {
        if (key === 'usageRanking.reasoningCost') return `${params?.unit ?? 'USD'} Used`
        const labels: Record<string, string> = {
          'usageRanking.timeRange': 'Time range',
          'common.refresh': 'Refresh',
          'usageRanking.listTitle': 'Ranking List',
          'usageRanking.limitHint': `Showing top ${params?.limit ?? ''}`,
          'usageRanking.totalTokens': 'Total Tokens',
          'usageRanking.requests': 'Requests',
          'usageRanking.emptyTitle': 'No ranking data',
          'usageRanking.emptyDescription': 'No data',
          'usageRanking.loadError': 'Failed to load ranking',
        }
        return labels[key] ?? key
      },
    }),
  }
})

const AppLayoutStub = { template: '<div><slot /></div>' }

function mountView() {
  return mount(UsageRankingView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        DateRangePicker: true,
        LoadingSpinner: true,
        Icon: true,
      },
    },
  })
}

describe('UsageRankingView', () => {
  beforeEach(() => {
    getRanking.mockReset()
  })

  it('renders only the fields explicitly exposed by the ranking response', async () => {
    getRanking.mockResolvedValue({
      ranking: [
        {
          rank: 1,
          user_id: 7,
          display_name: 'ranked-user',
          avatar_url: '',
          requests: 8,
          total_tokens: 370,
          actual_cost: 12.5,
        },
      ],
      total_actual_cost: 12.5,
      sort_by: 'actual_cost',
      show_total_tokens: false,
      show_requests: false,
      show_actual_cost: true,
      start_date: '2026-08-17',
      end_date: '2026-08-17',
      limit: 20,
    })

    const wrapper = mountView()
    await flushPromises()

    expect(getRanking).toHaveBeenCalledOnce()
    expect(wrapper.text()).toContain('$12.5000')
    expect(wrapper.text()).toContain('USD Used')
    expect(wrapper.text()).not.toContain('Total Tokens')
    expect(wrapper.text()).not.toContain('Requests')
    expect(wrapper.html()).not.toContain('370')
  })

  it('keeps desktop metric columns aligned when values have different lengths', async () => {
    getRanking.mockResolvedValue({
      ranking: [
        {
          rank: 1,
          user_id: 7,
          display_name: 'first-user',
          avatar_url: '',
          requests: 8422,
          total_tokens: 610_000_000,
          actual_cost: 4264.3676,
        },
        {
          rank: 2,
          user_id: 8,
          display_name: 'second-user',
          avatar_url: '',
          requests: 1090,
          total_tokens: 350_000_000,
          actual_cost: 451.3939,
        },
      ],
      total_actual_cost: 4715.7615,
      sort_by: 'total_tokens',
      show_total_tokens: true,
      show_requests: true,
      show_actual_cost: true,
      start_date: '2026-08-17',
      end_date: '2026-08-17',
      limit: 20,
    })

    const wrapper = mountView()
    await flushPromises()

    const metricRows = wrapper.findAll('[data-testid="usage-ranking-row-metrics"]')
    expect(metricRows).toHaveLength(2)

    for (const row of metricRows) {
      const columns = row.findAll(':scope > div')
      expect(columns.map((column) => column.attributes('class'))).toEqual([
        'sm:w-[130px]',
        'sm:w-[110px]',
        'sm:w-[140px]',
      ])
    }
  })
})
