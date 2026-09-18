import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import AdminPaymentDashboardView from '../AdminPaymentDashboardView.vue'

const { mockGetDashboard } = vi.hoisted(() => {
  vi.stubGlobal('localStorage', {
    getItem: vi.fn(() => null),
    setItem: vi.fn(),
    removeItem: vi.fn(),
  })

  return {
    mockGetDashboard: vi.fn()
  }
})

vi.mock('@/api/admin/payment', () => ({
  default: {
    getDashboard: (...args: unknown[]) => mockGetDashboard(...args)
  },
  adminPaymentAPI: {
    getDashboard: (...args: unknown[]) => mockGetDashboard(...args)
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn()
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

function mountView() {
  return mount(AdminPaymentDashboardView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot name="page-heading-actions" /><slot /></div>' },
        LoadingSpinner: true,
        Icon: true,
        OrderStatsCards: true,
        DailyRevenueChart: true,
        PurchaseDistributionChart: true,
        DateRangePicker: {
          props: ['startDate', 'endDate'],
          emits: ['update:startDate', 'update:endDate', 'change'],
          template: `
            <button
              class="date-range-picker"
              @click="$emit('change', { startDate: '2026-05-01', endDate: '2026-05-09', preset: null })"
            >DateRangePicker</button>
          `
        }
      }
    }
  })
}

describe('AdminPaymentDashboardView', () => {
  beforeEach(() => {
    mockGetDashboard.mockReset()
    mockGetDashboard.mockResolvedValue({
      data: {
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
    })
  })

  it('loads dashboard with last 30 days date range by default', async () => {
    mountView()
    await flushPromises()

    const now = new Date()
    const start = new Date()
    start.setDate(start.getDate() - 29)

    expect(mockGetDashboard).toHaveBeenCalledWith({
      start_date: formatLocalDate(start),
      end_date: formatLocalDate(now)
    })
  })

  it('reloads dashboard when date range changes', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.find('.date-range-picker').trigger('click')
    await flushPromises()

    expect(mockGetDashboard).toHaveBeenLastCalledWith({
      start_date: '2026-05-01',
      end_date: '2026-05-09'
    })
  })

  it('hides USD amounts while preserving other currencies and wallet order counts', async () => {
    mockGetDashboard.mockResolvedValueOnce({
      data: {
        today_amount: { CNY: 70, USD: 10 },
        total_amount: { CNY: 70, USD: 10 },
        today_count: 2,
        total_count: 2,
        avg_amount: { CNY: 70, USD: 10 },
        avg_reasoning_point_purchase_unit_price: 0,
        reasoning_point_purchase_order_count: 0,
        daily_series: [{ date: '2026-09-16', amount: { CNY: 70, USD: 10 }, count: 2 }],
        payment_methods: [
          { type: 'alipay', amount: { CNY: 70 }, count: 1 },
          { type: 'balance', amount: { USD: 10 }, count: 1 }
        ],
        purchase_distribution: [
          { type: 'subscription', label: '专业版', plan_id: 7, currency: 'CNY', amount: 70, count: 1 },
          { type: 'subscription', label: '专业版', plan_id: 7, currency: 'USD', amount: 10, count: 1 }
        ],
        top_users: {
          CNY: [{ user_id: 2, email: 'payer@example.com', amount: 70 }],
          USD: [{ user_id: 1, email: 'wallet@example.com', amount: 10 }]
        }
      }
    })
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('payment.methods.balance')
    expect(wrapper.text()).toContain('payment.methods.alipay')
    expect(wrapper.text()).toContain('payment.admin.paymentStatisticsHint')
    expect(wrapper.text()).not.toContain('$10.00')
    expect(wrapper.text()).toContain('70.00')
    expect(wrapper.text()).toContain('payer@example.com')
    expect(wrapper.text()).not.toContain('wallet@example.com')
    expect(wrapper.find('.bg-amber-500').exists()).toBe(true)
    expect(wrapper.findComponent({ name: 'OrderStatsCards' }).props('stats').total_count).toBe(2)
    expect(wrapper.findComponent({ name: 'OrderStatsCards' }).props('stats').total_amount).toEqual({ CNY: 70 })
    expect(wrapper.findComponent({ name: 'DailyRevenueChart' }).props('data')).toEqual([
      { date: '2026-09-16', amount: { CNY: 70 }, count: 2 }
    ])
    expect(wrapper.findComponent({ name: 'OrderStatsCards' }).props('stats').payment_methods[1]).toEqual({
      type: 'balance', amount: {}, count: 1
    })
    expect(wrapper.findComponent({ name: 'PurchaseDistributionChart' }).props('items')).toHaveLength(2)
    wrapper.unmount()
  })
})
