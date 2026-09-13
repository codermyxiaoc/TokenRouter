import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import EndpointDistributionChart from '../EndpointDistributionChart.vue'

const messages: Record<string, string> = {
  'usage.endpointDistribution': 'Endpoint Distribution',
  'usage.endpoint': 'Endpoint',
  'usage.inbound': 'Inbound',
  'usage.upstream': 'Upstream',
  'usage.path': 'Path',
  'admin.dashboard.requests': 'Requests',
  'admin.dashboard.tokens': 'Tokens',
  'admin.dashboard.actual': 'Actual',
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

describe('EndpointDistributionChart', () => {
  const endpointStats = [
    {
      endpoint: '/v1/responses',
      requests: 9,
      total_tokens: 1200,
      cost: 1.8,
      actual_cost: 0.1,
    },
    {
      endpoint: '/v1/messages',
      requests: 4,
      total_tokens: 600,
      cost: 0.7,
      actual_cost: 0.9,
    },
  ]

  it('renders a doughnut chart ordered by tokens by default', () => {
    const wrapper = mount(EndpointDistributionChart, {
      props: {
        endpointStats,
      },
      global: {
        stubs: {
          LoadingSpinner: true,
        },
      },
    })

    expect(wrapper.find('.bar-chart-data').exists()).toBe(false)
    const chartData = JSON.parse(wrapper.find('.chart-data').text())
    expect(chartData.labels).toEqual(['/v1/responses', '/v1/messages'])
    expect(chartData.datasets[0].data[0]).toBeCloseTo(1 + Math.log10(2))
    expect(chartData.datasets[0].data[1]).toBe(1)
  })

  it('renders a bar chart instead of doughnut when chartType is bar', () => {
    const wrapper = mount(EndpointDistributionChart, {
      props: {
        endpointStats,
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
    expect(chartData.labels).toEqual(['/v1/responses', '/v1/messages'])
    expect(chartData.datasets[0].data).toEqual([1200, 600])

    const options = (wrapper.vm as any).$?.setupState.barOptions
    expect(options.plugins.tooltip.enabled).toBe(false)
    expect(options.plugins.tooltip.external).toBeTypeOf('function')
    expect(options.scales.x.ticks.display).toBe(false)
    expect(options.scales.y.type).toBe('logarithmic')
    expect(options.scales.y.ticks.display).toBe(false)
    expect(options.scales.y.grid.display).toBe(false)
    const label = options.plugins.tooltip.callbacks.label({
      label: '/v1/responses',
      dataIndex: 0,
    })
    expect(label).toBe('/v1/responses: 1.20K (66.7%)')
  })

  it('reorders rows by actual cost in actual cost mode', () => {
    const wrapper = mount(EndpointDistributionChart, {
      props: {
        endpointStats,
        metric: 'actual_cost',
      },
      global: {
        stubs: {
          LoadingSpinner: true,
        },
      },
    })

    const chartData = JSON.parse(wrapper.find('.chart-data').text())
    expect(chartData.labels).toEqual(['/v1/messages', '/v1/responses'])
    expect(chartData.datasets[0].data[0]).toBeCloseTo(1 + Math.log10(9))
    expect(chartData.datasets[0].data[1]).toBe(1)

    const rows = wrapper.findAll('tbody tr')
    expect(rows[0].text()).toContain('/v1/messages')
    expect(rows[1].text()).toContain('/v1/responses')
  })
})
