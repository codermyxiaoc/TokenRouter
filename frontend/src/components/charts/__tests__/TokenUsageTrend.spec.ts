import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import TokenUsageTrend from '../TokenUsageTrend.vue'
import { setTheme } from '@/composables/useTheme'

const messages: Record<string, string> = {
  'admin.dashboard.tokenUsageTrend': 'Token Usage Trend',
  'admin.dashboard.noDataAvailable': 'No data available'
}

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => messages[key] ?? key
    })
  }
})

vi.mock('vue-chartjs', () => ({
  Line: {
    props: ['data', 'options'],
      template: `
      <div>
        <div class="chart-data">{{ JSON.stringify(data) }}</div>
        <div class="chart-options">{{ JSON.stringify(options) }}</div>
        <div class="tooltip-title">{{ options.plugins.tooltip.callbacks.title?.([{ dataIndex: 0 }]) }}</div>
        <div class="tooltip-footer">{{ options.plugins.tooltip.callbacks.footer?.([{ dataIndex: 0 }]) }}</div>
      </div>
    `
  }
}))

describe('TokenUsageTrend', () => {
  beforeEach(() => {
    setTheme(false)
  })

  it('calculates cache hit rate against all prompt tokens', () => {
    const wrapper = mount(TokenUsageTrend, {
      props: {
        trendData: [
          {
            date: '2026-05-08',
            requests: 1,
            input_tokens: 500,
            output_tokens: 100,
            cache_creation_tokens: 0,
            cache_read_tokens: 1500,
            cost: 0.01,
            actual_cost: 0.005
          }
        ]
      },
      global: {
        stubs: {
          LoadingSpinner: true
        }
      }
    })

    const chartData = JSON.parse(wrapper.find('.chart-data').text())
    const options = (wrapper.vm as any).$?.setupState.lineOptions
    expect(options.plugins.tooltip.enabled).toBe(false)
    expect(options.plugins.tooltip.external).toBeTypeOf('function')
    const hitRateDataset = chartData.datasets.find(
      (ds: any) => ds.label === 'Cached Input %'
    )
    // 命中率 = 1500 / (500 + 1500 + 0) * 100 = 75%
    expect(hitRateDataset.data[0]).toBe(75)
    // 缓存命中率曲线使用实线，避免与用户要求的虚线样式混淆。
    expect(hitRateDataset.borderDash).toBeUndefined()
  })

  it('returns 0 hit rate when all prompt tokens are zero', () => {
    const wrapper = mount(TokenUsageTrend, {
      props: {
        trendData: [
          {
            date: '2026-05-08',
            requests: 0,
            input_tokens: 0,
            output_tokens: 0,
            cache_creation_tokens: 0,
            cache_read_tokens: 0,
            cost: 0,
            actual_cost: 0
          }
        ]
      },
      global: {
        stubs: {
          LoadingSpinner: true
        }
      }
    })

    const chartData = JSON.parse(wrapper.find('.chart-data').text())
    const hitRateDataset = chartData.datasets.find(
      (ds: any) => ds.label === 'Cached Input %'
    )
    expect(hitRateDataset.data[0]).toBe(0)
  })

  it('includes cache_creation_tokens in denominator for Anthropic models', () => {
    const wrapper = mount(TokenUsageTrend, {
      props: {
        trendData: [
          {
            date: '2026-05-08',
            requests: 1,
            input_tokens: 200,
            output_tokens: 50,
            cache_creation_tokens: 300,
            cache_read_tokens: 500,
            cost: 0.02,
            actual_cost: 0.01
          }
        ]
      },
      global: {
        stubs: {
          LoadingSpinner: true
        }
      }
    })

    const chartData = JSON.parse(wrapper.find('.chart-data').text())
    const hitRateDataset = chartData.datasets.find(
      (ds: any) => ds.label === 'Cached Input %'
    )
    // 命中率 = 500 / (200 + 500 + 300) * 100 = 50%
    expect(hitRateDataset.data[0]).toBe(50)
  })

  it('updates chart text colors after switching from dark to light mode', async () => {
    setTheme(true)
    const wrapper = mount(TokenUsageTrend, {
      props: {
        trendData: [
          {
            date: '2026-07-25',
            requests: 1,
            input_tokens: 100,
            output_tokens: 20,
            cache_creation_tokens: 0,
            cache_read_tokens: 0,
            cost: 0.01,
            actual_cost: 0.005
          }
        ]
      },
      global: {
        stubs: {
          LoadingSpinner: true
        }
      }
    })

    expect(JSON.parse(wrapper.find('.chart-options').text()).plugins.legend.labels.color).toBe(
      '#E4E4E7'
    )

    setTheme(false)
    await wrapper.vm.$nextTick()

    const lightOptions = JSON.parse(wrapper.find('.chart-options').text())
    // 浅色主题下使用深色文字，避免图例和坐标轴融入白色背景。
    expect(lightOptions.plugins.legend.labels.color).toBe('#3F3F46')
    expect(lightOptions.scales.x.ticks.color).toBe('#3F3F46')
    expect(lightOptions.scales.y.ticks.color).toBe('#3F3F46')
  })

  it('uses compact hour labels while keeping the full date in the tooltip', () => {
    const wrapper = mount(TokenUsageTrend, {
      props: {
        granularity: 'hour',
        trendData: [
          {
            date: '2026-08-24 08:00',
            requests: 1,
            input_tokens: 100,
            output_tokens: 20,
            cache_creation_tokens: 0,
            cache_read_tokens: 0,
            cost: 0.01,
            actual_cost: 0.005
          }
        ]
      },
      global: {
        stubs: {
          LoadingSpinner: true
        }
      }
    })

    const chartData = JSON.parse(wrapper.find('.chart-data').text())
    expect(chartData.labels).toEqual(['08:00'])
    expect(wrapper.find('.tooltip-title').text()).toBe('2026-08-24 08:00')
  })

  it('uses month-day labels for daily granularity while keeping the full date in the tooltip', () => {
    const wrapper = mount(TokenUsageTrend, {
      props: {
        granularity: 'day',
        trendData: [
          {
            date: '2026-08-24',
            requests: 1,
            input_tokens: 100,
            output_tokens: 20,
            cache_creation_tokens: 0,
            cache_read_tokens: 0,
            cost: 0.01,
            actual_cost: 0.005
          }
        ]
      },
      global: {
        stubs: {
          LoadingSpinner: true
        }
      }
    })

    const chartData = JSON.parse(wrapper.find('.chart-data').text())
    expect(chartData.labels).toEqual(['08-24'])
    expect(wrapper.find('.tooltip-title').text()).toBe('2026-08-24')
  })

  it('omits standard cost from the tooltip when disabled', () => {
    const wrapper = mount(TokenUsageTrend, {
      props: {
        showStandardCost: false,
        trendData: [
          {
            date: '2026-08-24',
            requests: 1,
            input_tokens: 100,
            output_tokens: 20,
            cache_creation_tokens: 0,
            cache_read_tokens: 0,
            cost: 0.01,
            actual_cost: 0.005
          }
        ]
      },
      global: {
        stubs: {
          LoadingSpinner: true
        }
      }
    })

    expect(wrapper.find('.tooltip-footer').text()).toContain('Actual:')
    expect(wrapper.find('.tooltip-footer').text()).not.toContain('Standard:')
  })
})
