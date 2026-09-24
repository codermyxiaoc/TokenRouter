import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import type { PublicSettings, UserSubscription } from '@/types'
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
const originalConfig = window.__APP_CONFIG__
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
    window.__APP_CONFIG__ = { server_timezone: 'Asia/Shanghai' } as PublicSettings
    mocks.extend.mockResolvedValue({})
  })
  afterEach(() => { vi.useRealTimers(); window.__APP_CONFIG__ = originalConfig })

  for (const period of periods) {
    for (const previous of ['null', 'expired'] as const) {
      it(`refreshes admin ${period} reset display after extending a ${previous} window`, async () => {
        const oldStart = previous === 'null' ? null : at(-spans[period] - day)
        const before = { ...subscription(period, oldStart), [`${period}_reset_count`]: 12 }
        const after = { ...subscription(period, recoveredStarts[period], at(70 * day)), [`${period}_reset_count`]: 12 }
        mocks.list.mockResolvedValueOnce({ items: [before], total: 1, pages: 1 })
          .mockResolvedValueOnce({ items: [after], total: 1, pages: 1 })
        const wrapper = mount(AdminSubscriptionsView, { global: { stubs } })
        try {
          await flushPromises()
          expect(wrapper.text()).not.toContain('admin.subscriptions.resetIn')
          expect(wrapper.get(`[data-testid="admin-quota-reset-count-${period}"]`).text()).toContain('"count":12')
          await wrapper.findAll('button').find(button => button.text() === 'admin.subscriptions.adjust')!.trigger('click')
          await wrapper.get('#extend-subscription-form input[type="number"]').setValue(70)
          await wrapper.get('#extend-subscription-form').trigger('submit')
          await flushPromises()
          expect(mocks.extend).toHaveBeenCalledWith(41, { days: 70 })
          expect(mocks.list).toHaveBeenCalledTimes(2)
          const resets = wrapper.findAll('.reset-info')
          expect(resets).toHaveLength(1)
          expect(resets[0].text()).toContain('admin.subscriptions.resetIn')
          expect(wrapper.get(`[data-testid="admin-quota-reset-count-${period}"]`).text()).toContain('"count":12')
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

  it('shows all configured admin reset counts without requiring usage or an active window anchor', async () => {
    const row = {
      ...subscription('daily', null, at(70 * day)),
      weekly_limit_usd: 60, monthly_limit_usd: 150,
      daily_reset_count: 17, weekly_reset_count: 3, monthly_reset_count: 2
    }
    mocks.list.mockResolvedValue({ items: [row], total: 1, pages: 1 })
    const wrapper = mount(AdminSubscriptionsView, { global: { stubs } })
    try {
      await flushPromises()
      expect(wrapper.get('[data-testid="admin-quota-reset-count-daily"]').text()).toContain('"count":17')
      expect(wrapper.get('[data-testid="admin-quota-reset-count-weekly"]').text()).toContain('"count":3')
      expect(wrapper.get('[data-testid="admin-quota-reset-count-monthly"]').text()).toContain('"count":2')
      expect(wrapper.findAll('.reset-info')).toHaveLength(0)
      expect(wrapper.get('[data-testid="admin-quota-reset-count-daily"]').attributes('title')).toBe('userSubscriptions.resetCountHint')
    } finally { wrapper.unmount() }
  })

  it('defaults missing admin counts to zero and hides unconfigured windows', async () => {
    mocks.list.mockResolvedValue({ items: [subscription('daily', null)], total: 1, pages: 1 })
    const wrapper = mount(AdminSubscriptionsView, { global: { stubs } })
    try {
      await flushPromises()
      expect(wrapper.get('[data-testid="admin-quota-reset-count-daily"]').text()).toContain('"count":0')
      expect(wrapper.find('[data-testid="admin-quota-reset-count-weekly"]').exists()).toBe(false)
      expect(wrapper.find('[data-testid="admin-quota-reset-count-monthly"]').exists()).toBe(false)
    } finally { wrapper.unmount() }
  })

  it('refreshes due admin counts from the server while usage stays zero', async () => {
    const before = { ...subscription('daily', recoveredStarts.daily, at(70 * day)), daily_reset_count: 4 }
    const after = { ...before, daily_window_start: at(day / 2), daily_reset_count: 5 }
    mocks.list.mockResolvedValueOnce({ items: [before], total: 1, pages: 1 })
      .mockResolvedValueOnce({ items: [after], total: 1, pages: 1 })
    const wrapper = mount(AdminSubscriptionsView, { global: { stubs } })
    try {
      await flushPromises()
      expect(wrapper.get('[data-testid="admin-quota-reset-count-daily"]').text()).toContain('"count":4')
      // 浏览器不独立猜测刷新次数；到点后重新获取服务器的窗口与计数。
      vi.setSystemTime(now + day)
      await wrapper.get('button[title="common.refresh"]').trigger('click')
      await flushPromises()
      expect(wrapper.get('[data-testid="admin-quota-reset-count-daily"]').text()).toContain('"count":5')
      expect(wrapper.get('.usage-amount').text()).toMatch(/^0\s*\/\s*10$/)
    } finally { wrapper.unmount() }
  })

  for (const page of ['admin', 'user'] as const) {
    for (const period of periods) {
      it(`shows ${page} ${period} reset near expiry without an outer quota or another full cycle`, async () => {
        const row = subscription(period, recoveredStarts[period], at((period === 'daily' ? 1 : period === 'weekly' ? 8 : 31) * day))
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

    const resetCases: {
      name: string; period: Period; starts: string; anchor: string; expiry?: string; now?: string;
      timezone?: string; days?: number; hours: number; minutes?: number
    }[] = [
      { name: '历史非零点日锚点', period: 'daily', starts: '2026-09-01T10:18:00+08:00', anchor: '2026-09-21T08:00:00+08:00', hours: 12 },
      { name: '首次被截断的周锚点', period: 'weekly', starts: '2026-09-21T10:18:00+08:00', anchor: '2026-09-21T00:00:00+08:00', days: 6, hours: 22 },
      { name: '首次被截断的月锚点', period: 'monthly', starts: '2026-09-21T10:18:00+08:00', anchor: '2026-09-21T00:00:00+08:00', days: 29, hours: 22 },
      { name: '后续手动重置零点', period: 'weekly', starts: '2026-09-01T10:18:00+08:00', anchor: '2026-09-21T00:00:00+08:00', days: 6, hours: 12 },
      { name: '1343在9月30日重置', period: 'weekly', starts: '2026-09-05T10:18:00+08:00', anchor: '2026-09-23T00:00:00+08:00', expiry: '2026-10-05T10:18:00+08:00', now: '2026-09-23T12:00:00+08:00', days: 6, hours: 12 },
      { name: '异地浏览器下的项目夏令时', period: 'daily', starts: '2026-03-01T10:18:00-05:00', anchor: '2026-03-08T00:00:00-05:00', expiry: '2026-04-05T10:18:00-04:00', now: '2026-03-08T12:00:00-04:00', timezone: 'America/New_York', hours: 12 }
    ]
    for (const test of resetCases) {
      it(`${page} 正确展示${test.name}且保留服务端次数和用量`, async () => {
        if (test.now) vi.setSystemTime(new Date(test.now))
        if (test.timezone) window.__APP_CONFIG__ = { server_timezone: test.timezone } as PublicSettings
        const row = {
          ...subscription(test.period, test.anchor, test.expiry ?? at(100 * day)), starts_at: test.starts,
          [`${test.period}_reset_count`]: 17, [`${test.period}_usage_usd`]: 3.25
        }
        mocks.list.mockResolvedValue({ items: [row], total: 1, pages: 1 })
        mocks.mine.mockResolvedValue([row])
        const wrapper = mount(page === 'admin' ? AdminSubscriptionsView : UserSubscriptionsView, { global: { stubs } })
        try {
          await flushPromises()
          const text = page === 'admin' ? wrapper.get('.reset-info').text() : wrapper.text()
          const params = page === 'admin'
            ? test.days ? { days: test.days, hours: test.hours } : { hours: test.hours, minutes: test.minutes ?? 0 }
            : { time: test.days ? `${test.days}d ${test.hours}h` : `${test.hours}h ${test.minutes ?? 0}m` }
          expect(text).toContain(JSON.stringify(params))
          expect(text).not.toContain('quotaEndsIn')
          expect(wrapper.get(`[data-testid="${page === 'admin' ? 'admin-' : ''}quota-reset-count-${test.period}"]`).text()).toContain('"count":17')
          expect(wrapper.text()).toContain('3.25')
          expect(row[`${test.period}_window_start`]).toBe(test.anchor)
        } finally { wrapper.unmount() }
      })
    }
  }
})
