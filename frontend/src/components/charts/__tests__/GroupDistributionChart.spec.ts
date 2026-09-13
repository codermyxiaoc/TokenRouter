import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import GroupDistributionChart from '../GroupDistributionChart.vue'

const messages: Record<string, string> = {
  'admin.dashboard.groupDistribution': 'Group Distribution',
  'admin.dashboard.group': 'Group',
  'admin.dashboard.noGroup': 'No Group',
  'admin.dashboard.requests': 'Requests',
  'admin.dashboard.tokens': 'Tokens',
  'admin.dashboard.actual': 'Actual',
  'admin.dashboard.accountCost': 'Account Cost',
  'admin.dashboard.standard': 'Standard',
  'admin.dashboard.metricTokens': 'By Tokens',
  'admin.dashboard.metricActualCost': 'By Actual Cost',
  'admin.dashboard.noDataAvailable': 'No data available',
}

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => messages[key] ?? key,
    }),
  }
})

vi.mock('vue-chartjs', () => ({
  Doughnut: {
    props: ['data'],
    template: '<div class="chart-data">{{ JSON.stringify(data) }}</div>',
  },
  Bar: {
    props: ['data'],
    template: '<div class="bar-chart-data">{{ JSON.stringify(data) }}</div>',
  },
}))

describe('GroupDistributionChart', () => {
  const groupStats = [
    {
      group_id: 1,
      group_name: 'group-a',
      requests: 9,
      total_tokens: 1200,
      cost: 1.8,
      actual_cost: 0.1,
    },
    {
      group_id: 2,
      group_name: 'group-b',
      requests: 4,
      total_tokens: 600,
      cost: 0.7,
      actual_cost: 0.9,
    },
  ]

  it('uses total_tokens and token ordering by default', () => {
    const wrapper = mount(GroupDistributionChart, {
      props: {
        groupStats,
      },
      global: {
        stubs: {
          LoadingSpinner: true,
        },
      },
    })

    const chartData = JSON.parse(wrapper.find('.chart-data').text())
    expect(chartData.labels).toEqual(['group-a', 'group-b'])
    expect(chartData.datasets[0].data[0]).toBeCloseTo(1 + Math.log10(2))
    expect(chartData.datasets[0].data[1]).toBe(1)

    const chartTableLayout = wrapper.find('table').element.parentElement?.parentElement
    expect(chartTableLayout?.className).toContain('sm:items-start')

    const rows = wrapper.findAll('tbody tr')
    expect(rows[0].text()).toContain('group-a')
    expect(rows[1].text()).toContain('group-b')

    const options = (wrapper.vm as any).$?.setupState.doughnutOptions
    expect(options.plugins.tooltip.enabled).toBe(false)
    expect(options.plugins.tooltip.external).toBeTypeOf('function')
    const label = options.plugins.tooltip.callbacks.label({
      label: 'group-a',
      dataIndex: 0,
    })
    expect(label).toBe('group-a: 1.20K (66.7%)')
  })

  it('uses actual_cost and reorders rows in actual cost mode', () => {
    const wrapper = mount(GroupDistributionChart, {
      props: {
        groupStats,
        metric: 'actual_cost',
      },
      global: {
        stubs: {
          LoadingSpinner: true,
        },
      },
    })

    const chartData = JSON.parse(wrapper.find('.chart-data').text())
    expect(chartData.labels).toEqual(['group-b', 'group-a'])
    expect(chartData.datasets[0].data[0]).toBeCloseTo(1 + Math.log10(9))
    expect(chartData.datasets[0].data[1]).toBe(1)

    const rows = wrapper.findAll('tbody tr')
    expect(rows[0].text()).toContain('group-b')
    expect(rows[1].text()).toContain('group-a')

    const options = (wrapper.vm as any).$?.setupState.doughnutOptions
    const label = options.plugins.tooltip.callbacks.label({
      label: 'group-b',
      dataIndex: 0,
    })
    expect(label).toBe('group-b: $0.900 (90.0%)')
  })

  it('can hide account cost for user usage stats without account_cost', () => {
    const wrapper = mount(GroupDistributionChart, {
      props: {
        groupStats,
        showAccountCost: false,
      },
      global: {
        stubs: {
          LoadingSpinner: true,
        },
      },
    })

    expect(wrapper.text()).not.toContain('Account Cost')
    expect(wrapper.findAll('thead th')).toHaveLength(5)
    expect(wrapper.findAll('tbody tr')[0].findAll('td')).toHaveLength(5)
  })

  it('renders a horizontal log-scale bar chart instead of doughnut when chartType is bar', () => {
    const wrapper = mount(GroupDistributionChart, {
      props: {
        groupStats,
        chartType: 'bar',
      },
      global: {
        stubs: {
          LoadingSpinner: true,
        },
      },
    })

    expect(wrapper.find('.chart-data').exists()).toBe(false)
    const chartData = JSON.parse(wrapper.find('.bar-chart-data').text())
    expect(chartData.labels).toEqual(['group-a', 'group-b'])
    expect(chartData.datasets[0].data).toEqual([1200, 600])

    const options = (wrapper.vm as any).$?.setupState.barOptions
    expect(options.plugins.tooltip.enabled).toBe(false)
    expect(options.plugins.tooltip.external).toBeTypeOf('function')
    expect(options.indexAxis).toBe('y')
    expect(options.scales.x.type).toBe('logarithmic')
    expect(options.scales.x.ticks.display).toBe(false)
    expect(options.scales.x.grid.display).toBe(false)

    const truncate = options.scales.y.ticks.callback
    expect(truncate.call({ getLabelForValue: () => 'group-a' }, 0)).toBe('group-a')
    expect(truncate.call({ getLabelForValue: () => 'a-very-long-group-name' }, 0)).toBe('a-very-long-…')

    const label = options.plugins.tooltip.callbacks.label({
      label: 'group-a',
      dataIndex: 0,
    })
    expect(label).toBe('group-a: 1.20K (66.7%)')
  })

  it('uses logarithmic doughnut slices while tooltip keeps raw value and real percentage', () => {
    const wrapper = mount(GroupDistributionChart, {
      props: {
        groupStats: [
          { group_id: 1, group_name: 'big', requests: 1, total_tokens: 1_000_000, cost: 1, actual_cost: 1 },
          { group_id: 2, group_name: 'small', requests: 1, total_tokens: 100, cost: 1, actual_cost: 1 },
        ],
      },
      global: {
        stubs: {
          LoadingSpinner: true,
        },
      },
    })

    const chartData = JSON.parse(wrapper.find('.chart-data').text())
    expect(chartData.labels).toEqual(['big', 'small'])
    // 扇区按 log10 压缩，小占比分组仍获得可见角度
    expect(chartData.datasets[0].data).toEqual([5, 1])

    const options = (wrapper.vm as any).$?.setupState.doughnutOptions
    const label = options.plugins.tooltip.callbacks.label({
      label: 'small',
      dataIndex: 1,
    })
    expect(label).toBe('small: 100 (0.0%)')
  })

  it('labels entries without a group as No Group instead of 0', () => {
    const wrapper = mount(GroupDistributionChart, {
      props: {
        groupStats: [
          { group_id: 0, group_name: '', requests: 1, total_tokens: 10, cost: 0, actual_cost: 0 },
          { group_id: 1, group_name: 'group-a', requests: 1, total_tokens: 20, cost: 0, actual_cost: 0 },
        ],
      },
      global: {
        stubs: {
          LoadingSpinner: true,
        },
      },
    })

    const chartData = JSON.parse(wrapper.find('.chart-data').text())
    expect(chartData.labels).toEqual(['group-a', 'No Group'])
  })
})
