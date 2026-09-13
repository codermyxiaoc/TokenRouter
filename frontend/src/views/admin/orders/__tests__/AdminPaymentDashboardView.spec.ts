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
})
