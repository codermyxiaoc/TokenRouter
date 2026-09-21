import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import type { UserSubscription } from '@/types'
import AdminSubscriptionsView from '../SubscriptionsView.vue'
import UserSubscriptionsView from '../../user/SubscriptionsView.vue'

const mocks = vi.hoisted(() => ({ list: vi.fn(), extend: vi.fn(), mine: vi.fn(), showError: vi.fn(), showSuccess: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: {
  subscriptions: { list: mocks.list, extend: mocks.extend },
  payment: { getPlans: vi.fn().mockResolvedValue({ data: [] }) }
} }))
vi.mock('@/api/subscriptions', () => ({ default: { getMySubscriptions: mocks.mine } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: mocks.showError, showSuccess: mocks.showSuccess }) }))
vi.mock('@/stores/subscriptions', () => ({ useSubscriptionStore: () => ({ fetchActiveSubscriptions: vi.fn() }) }))
vi.mock('@/composables/useBalanceDisplay', () => ({ useBalanceDisplay: () => ({ formatBalanceAmount: String }) }))
vi.mock('vue-router', async () => ({ ...await vi.importActual<typeof import('vue-router')>('vue-router'), useRouter: () => ({ push: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({
  t: (key: string, params?: Record<string, unknown>) => params ? `${key} ${JSON.stringify(params)}` : key
}) }))

const now = new Date('2026-09-21T12:00:00+08:00').getTime()
const day = 24 * 60 * 60 * 1000
const at = (offset: number) => new Date(now + offset).toISOString()
const periods = ['daily', 'weekly', 'monthly'] as const
type Period = typeof periods[number]

// 日窗口来自后端项目时区零点；周/月窗口保留各自的起点，不从到期时间反推。
const recoveredStarts: Record<Period, string> = {
  daily: new Date('2026-09-21T00:00:00+08:00').toISOString(), weekly: at(0), monthly: at(0)
}
const spans: Record<Period, number> = { daily: day, weekly: 7 * day, monthly: 30 * day }

function subscription(period: Period, windowStart: string | null, expiry = at(day / 24)): UserSubscription {
  return {
    id: 41, user_id: 7, plan_id: 11, status: 'active', starts_at: at(-60 * day), expires_at: expiry,
    daily_limit_usd: null, weekly_limit_usd: null, monthly_limit_usd: null,
    daily_usage_usd: 0, weekly_usage_usd: 0, monthly_usage_usd: 0,
    daily_window_start: null, weekly_window_start: null, monthly_window_start: null,
    created_at: at(-60 * day), updated_at: at(0),
    [`${period}_limit_usd`]: 10, [`${period}_window_start`]: windowStart,
    plan: { id: 11, name: 'Window recovery plan', description: '', price: 10, features: [], validity_days: 60,
      validity_unit: 'day', for_sale: true, sort_order: 0 }
  }
}

const stubs = {
  AppLayout: { template: '<div><slot /></div>' },
  TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
  DataTable: { props: ['data'], template: '<div><section v-for="row in data" :key="row.id"><slot name="cell-usage" :row="row" /><slot name="cell-actions" :row="row" /></section></div>' },
  BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
  Select: true, ConfirmDialog: true, EmptyState: true, Pagination: true, Icon: true, Teleport: true, RouterLink: true, GroupBadge: true
}

describe('subscription reset time after extension', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.list.mockReset()
    mocks.mine.mockReset()
    localStorage.clear()
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(now)
    mocks.extend.mockResolvedValue({})
  })
  afterEach(() => { vi.useRealTimers() })

  for (const period of periods) {
    for (const previous of ['null', 'expired'] as const) {
      it(`refreshes admin ${period} reset display after extending a ${previous} window`, async () => {
        const oldStart = previous === 'null' ? null : at(-spans[period] - day)
        const before = subscription(period, oldStart)
        const after = subscription(period, recoveredStarts[period], at(70 * day))
        mocks.list.mockResolvedValueOnce({ items: [before], total: 1, pages: 1 })
          .mockResolvedValueOnce({ items: [after], total: 1, pages: 1 })
        const wrapper = mount(AdminSubscriptionsView, { global: { stubs } })
        try {
          await flushPromises()
          expect(wrapper.text()).not.toContain('admin.subscriptions.resetIn')
          await wrapper.findAll('button').find(button => button.text() === 'admin.subscriptions.adjust')!.trigger('click')
          await wrapper.get('#extend-subscription-form input[type="number"]').setValue(70)
          await wrapper.get('#extend-subscription-form').trigger('submit')
          await flushPromises()
          expect(mocks.extend).toHaveBeenCalledWith(41, { days: 70 })
          expect(mocks.list).toHaveBeenCalledTimes(2)
          const resets = wrapper.findAll('.reset-info')
          expect(resets).toHaveLength(1)
          expect(resets[0].text()).toContain('admin.subscriptions.resetIn')
          if (period === 'daily') expect(resets[0].text()).toContain('"hours":12')
          else expect(resets[0].text()).toContain(`"days":${period === 'weekly' ? 7 : 30}`)
        } finally { wrapper.unmount() }
      })

      it(`shows user ${period} reset time on reopening after a ${previous} window is restored`, async () => {
        const before = subscription(period, previous === 'null' ? null : at(-spans[period] - day))
        const after = subscription(period, recoveredStarts[period], at(70 * day))
        mocks.mine.mockResolvedValueOnce([before]).mockResolvedValueOnce([after])
        const old = mount(UserSubscriptionsView, { global: { stubs } })
        try {
          await flushPromises()
          if (previous === 'null') expect(old.text()).not.toContain('userSubscriptions.resetIn')
          else expect(old.text()).toMatch(/userSubscriptions\.(windowNotActive|quotaEndsIn)/)
        } finally { old.unmount() }
        // 管理员与用户是独立页面；用户重新进入时读取后端刚恢复的窗口，不能沿用旧列表。
        const wrapper = mount(UserSubscriptionsView, { global: { stubs } })
        try {
          await flushPromises()
          expect(mocks.mine).toHaveBeenCalledTimes(2)
          expect(wrapper.text()).toContain('userSubscriptions.resetIn')
          expect(wrapper.text()).toContain(`"time":"${period === 'daily' ? '12h 0m' : period === 'weekly' ? '7d 0h' : '30d 0h'}"`)
          expect(wrapper.text()).not.toContain('userSubscriptions.quotaEndsIn')
        } finally { wrapper.unmount() }
      })
    }
  }

  for (const page of ['admin', 'user'] as const) {
    for (const period of ['daily', 'weekly'] as const) {
      it(`shows ${page} ${period} reset protected by a finite outer quota near expiry`, async () => {
        const row = subscription(period, recoveredStarts[period], at(period === 'daily' ? day : 8 * day))
        row.monthly_limit_usd = 100
        mocks.list.mockResolvedValue({ items: [row], total: 1, pages: 1 })
        mocks.mine.mockResolvedValue([row])
        const wrapper = mount(page === 'admin' ? AdminSubscriptionsView : UserSubscriptionsView, { global: { stubs } })
        try {
          await flushPromises()
          const text = page === 'admin' ? wrapper.get('.reset-info').text() : wrapper.text()
          expect(text).toContain(page === 'admin' ? 'admin.subscriptions.resetIn' : 'userSubscriptions.resetIn')
          expect(text).not.toContain(page === 'admin' ? 'admin.subscriptions.quotaEndsIn' : 'userSubscriptions.quotaEndsIn')
        } finally { wrapper.unmount() }
      })
    }
  }
})
