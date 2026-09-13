import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import TeamMemberUsageCharts from '../TeamMemberUsageCharts.vue'

const lineProps = vi.hoisted(() => vi.fn())
const barProps = vi.hoisted(() => vi.fn())

vi.mock('vue-chartjs', () => ({
  Line: {
    props: ['data', 'options'],
    setup(props: any) {
      lineProps(props)
      return () => null
    },
  },
  Bar: {
    props: ['data', 'options'],
    setup(props: any) {
      barProps(props)
      return () => null
    },
  },
}))

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/composables/useTheme', () => ({ useTheme: () => ({ isDark: { value: false } }) }))
vi.mock('@/composables/useBalanceDisplay', () => ({
  useBalanceDisplay: () => ({ formatBalanceAmount: (value: number) => `C${value}` }),
}))

describe('TeamMemberUsageCharts', () => {
  beforeEach(() => {
    lineProps.mockClear()
    barProps.mockClear()
  })

  it('aligns member daily costs and builds the spending share chart', () => {
    mount(TeamMemberUsageCharts, {
      props: {
        series: [
          {
            userID: 1,
            label: 'Owner',
            summary: {
              actual_cost: 1.5,
              request_count: 2,
              input_tokens: 10,
              output_tokens: 5,
              daily: [
                { date: '2026-07-26', actual_cost: 1, request_count: 1 },
                { date: '2026-07-27', actual_cost: 0.5, request_count: 1 },
              ],
            },
          },
          {
            userID: 2,
            label: 'Member',
            summary: {
              actual_cost: 0.25,
              request_count: 1,
              input_tokens: 3,
              output_tokens: 2,
              daily: [{ date: '2026-07-27', actual_cost: 0.25, request_count: 1 }],
            },
          },
        ],
      },
      global: { stubs: { LoadingSpinner: true } },
    })

    const lineData = lineProps.mock.calls.at(-1)?.[0].data
    const comparisonData = barProps.mock.calls.at(-1)?.[0].data
    expect(lineData.labels).toEqual(['2026-07-26', '2026-07-27'])
    expect(lineData.datasets[1].data).toEqual([0, 0.25])
    expect(comparisonData.labels).toEqual(['Owner', 'Member'])
    expect(comparisonData.datasets[0].data).toEqual([1.5, 0.25])
  })
})
