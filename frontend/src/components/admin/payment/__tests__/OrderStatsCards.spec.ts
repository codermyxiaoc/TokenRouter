import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import OrderStatsCards from '../OrderStatsCards.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  const labels: Record<string, string> = {
    'payment.admin.todayRevenue': '今日收入',
    'payment.admin.totalRevenue': '总收入',
    'payment.admin.todayOrders': '今日订单',
    'payment.admin.avgAmount': '平均金额',
    'payment.admin.orders': '订单'
  }
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, string>) => {
        if (key === 'payment.admin.avgBalanceUnitPurchaseUnitPrice') {
          return `平均${params?.unitName ?? ''}购买单价`
        }
        return labels[key] ?? key
      }
    })
  }
})

vi.mock('@/composables/useBalanceDisplay', () => ({
  useBalanceDisplay: () => ({
    balanceUnitName: 'AI Credits'
  })
}))

describe('OrderStatsCards', () => {
  it('renders average balance unit purchase unit price', () => {
    const wrapper = mount(OrderStatsCards, {
      props: {
        stats: {
          today_amount: { CNY: 12 },
          total_amount: { CNY: 120 },
          today_count: 2,
          total_count: 6,
          avg_amount: { CNY: 20 },
          avg_reasoning_point_purchase_unit_price: 0.3125,
          reasoning_point_purchase_order_count: 4,
          daily_series: [],
          payment_methods: [],
          purchase_distribution: [],
          top_users: {}
        }
      },
      global: {
        stubs: {
          Icon: true
        }
      }
    })

    expect(wrapper.text()).toContain('平均AI Credits购买单价')
    expect(wrapper.text()).toContain('¥0.3125/AI Credits')
    expect(wrapper.text()).toContain('4 订单')
  })

  it('shows zero amounts when the dashboard has no orders', () => {
    const wrapper = mount(OrderStatsCards, {
      props: {
        stats: {
          today_amount: {},
          total_amount: {},
          today_count: 0,
          total_count: 0,
          avg_amount: {},
          avg_reasoning_point_purchase_unit_price: 0,
          reasoning_point_purchase_order_count: 0,
          daily_series: [],
          payment_methods: [],
          purchase_distribution: [],
          top_users: {}
        }
      },
      global: {
        stubs: {
          Icon: true
        }
      }
    })

    expect(wrapper.text()).toContain('¥0.00')
  })
})
