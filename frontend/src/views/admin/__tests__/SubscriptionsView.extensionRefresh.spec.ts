import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import type { UserSubscription } from '@/types'
import AdminSubscriptionsView from '../SubscriptionsView.vue'
import UserSubscriptionsView from '../../user/SubscriptionsView.vue'

const mocks = vi.hoisted(() => ({ list: vi.fn(), extend: vi.fn(), bulkExtend: vi.fn(), mine: vi.fn(), showError: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: {
  subscriptions: { list: mocks.list, extend: mocks.extend, bulkExtend: mocks.bulkExtend },
  payment: { getPlans: vi.fn().mockResolvedValue({ data: [] }) }
} }))
vi.mock('@/api/subscriptions', () => ({ default: { getMySubscriptions: mocks.mine } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: mocks.showError, showSuccess: vi.fn() }) }))
vi.mock('@/stores/subscriptions', () => ({ useSubscriptionStore: () => ({ fetchActiveSubscriptions: vi.fn() }) }))
vi.mock('@/composables/useBalanceDisplay', () => ({ useBalanceDisplay: () => ({ formatBalanceAmount: String }) }))
vi.mock('vue-router', async () => ({ ...await vi.importActual<typeof import('vue-router')>('vue-router'), useRouter: () => ({ push: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({
  t: (key: string, params?: Record<string, unknown>) => params ? `${key} ${JSON.stringify(params)}` : key
}) }))

const now = Date.parse('2026-09-21T12:00:00+08:00')
const day = 86_400_000
const at = (offset: number) => new Date(now + offset).toISOString()
const periods = ['daily', 'weekly', 'monthly'] as const
type Period = typeof periods[number]
type Mode = 'single' | 'bulk'
const spans: Record<Period, number> = { daily: day, weekly: 7 * day, monthly: 30 * day }
const starts: Record<Period, string> = { daily: at(-day / 2), weekly: at(0), monthly: at(0) }
const stubs = {
  AppLayout: { template: '<div><slot /></div>' },
  TablePageLayout: { template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>' },
  DataTable: {
    name: 'DataTable', props: ['data', 'selectedKeys'], emits: ['update:selectedKeys'],
    template: '<div><section v-for="row in data" :key="row.id" :data-subscription-id="row.id"><slot name="cell-usage" :row="row" /><slot name="cell-actions" :row="row" /></section></div>'
  },
  BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
  Select: true, ConfirmDialog: true, EmptyState: true, Pagination: true, Icon: true, Teleport: true, RouterLink: true, GroupBadge: true
}

const row = (period: Period, windowStart: string | null): UserSubscription => ({
  id: 41, user_id: 7, plan_id: 11, status: 'active', starts_at: at(-60 * day), expires_at: at(day / 24),
  daily_limit_usd: null, weekly_limit_usd: null, monthly_limit_usd: null,
  daily_usage_usd: 1.125, weekly_usage_usd: 2.25, monthly_usage_usd: 3.5,
  daily_window_start: null, weekly_window_start: null, monthly_window_start: null,
  [`${period}_limit_usd`]: 10, [`${period}_window_start`]: windowStart,
  created_at: at(-60 * day), updated_at: at(0),
  plan: { id: 11, name: 'Extension interaction', description: '', price: 10, features: [], validity_days: 60,
    validity_unit: 'day', for_sale: true, sort_order: 0 }
})

// 测试只模拟 API 契约，后端持久化与实际到点重置由独立数据库测试验证。
function deferredMutation(before: UserSubscription, after: UserSubscription, mode: Mode) {
  let serverRows = [structuredClone(before)]
  let complete!: () => void
  mocks.list.mockImplementation(async () => ({ items: structuredClone(serverRows), total: 1, pages: 1 }))
  mocks.mine.mockImplementation(async () => structuredClone(serverRows))
  const mutation = new Promise<unknown>(resolve => {
    complete = () => {
      serverRows = [structuredClone(after)]
      resolve(mode === 'single' ? structuredClone(after) : { subscription_ids: [41], updated_count: 1 })
    }
  })
  ;(mode === 'single' ? mocks.extend : mocks.bulkExtend).mockReturnValueOnce(mutation)
  return complete
}

async function submitExtension(wrapper: VueWrapper, mode: Mode, days: number) {
  if (mode === 'single') {
    await wrapper.get('[data-subscription-id="41"]').findAll('button').find(button => button.text() === 'admin.subscriptions.adjust')!.trigger('click')
    await wrapper.get('#extend-subscription-form input[type="number"]').setValue(days)
    await wrapper.get('#extend-subscription-form').trigger('submit')
  } else {
    wrapper.getComponent({ name: 'DataTable' }).vm.$emit('update:selectedKeys', [41])
    await flushPromises()
    await wrapper.get('[data-test="bulk-extend"]').trigger('click')
    await wrapper.get('#bulk-subscription-days').setValue(days)
    await wrapper.get('#bulk-subscription-form').trigger('submit')
  }
  await flushPromises()
}

describe('订阅延期提交与重置时间刷新交互', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.extend.mockReset()
    mocks.bulkExtend.mockReset()
    localStorage.clear()
    sessionStorage.clear()
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(now)
  })
  afterEach(() => vi.useRealTimers())

  for (const mode of ['single', 'bulk'] as const) {
    for (const period of periods) {
      for (const previous of ['null', 'expired'] as const) {
        it(`${mode} 延期成功后刷新 ${period} 的 ${previous} 窗口，等待响应时不提前重置`, async () => {
          const before = row(period, previous === 'null' ? null : at(-spans[period] - day))
          const after = { ...before, expires_at: mode === 'single' ? at(70 * day) : at(70 * day + day / 24),
            [`${period}_window_start`]: starts[period], [`${period}_usage_usd`]: 0 }
          const complete = deferredMutation(before, after, mode)
          const admin = mount(AdminSubscriptionsView, { global: { stubs } })
          let user: VueWrapper | undefined
          try {
            await flushPromises()
            expect(admin.get('[data-subscription-id="41"]').text()).not.toContain('admin.subscriptions.resetIn')
            await submitExtension(admin, mode, 70)
            if (mode === 'single') expect(mocks.extend).toHaveBeenCalledWith(41, { days: 70 })
            else expect(mocks.bulkExtend).toHaveBeenCalledWith([41], 70, expect.stringContaining('subscription-bulk-'))
            expect(mocks.list).toHaveBeenCalledTimes(1)
            expect(admin.getComponent({ name: 'DataTable' }).props('data')).toEqual([before])
            expect(admin.get('[data-subscription-id="41"]').text()).not.toContain('admin.subscriptions.resetIn')
            complete()
            await flushPromises()
            expect(mocks.list).toHaveBeenCalledTimes(2)
            expect(admin.getComponent({ name: 'DataTable' }).props('data')).toEqual([after])
            expect(admin.get('.reset-info').text()).toContain('admin.subscriptions.resetIn')
            expect(admin.get('.usage-amount').text()).toMatch(/^0\s*\/\s*10$/)
            // 恢复某个窗口不应在前端顺便把其他窗口的累计值清零。
            for (const other of periods.filter(value => value !== period)) expect(after[`${other}_usage_usd`]).toBe(before[`${other}_usage_usd`])
            user = mount(UserSubscriptionsView, { global: { stubs } })
            await flushPromises()
            expect(user.text()).toContain('userSubscriptions.resetIn')
            expect(user.text()).toContain(`"time":"${period === 'daily' ? '12h 0m' : period === 'weekly' ? '7d 0h' : '30d 0h'}"`)
            expect(user.text()).not.toContain('userSubscriptions.quotaEndsIn')
            expect(mocks.showError).not.toHaveBeenCalled()
          } finally { admin.unmount(); user?.unmount() }
        })
      }
    }

    it(`${mode} 延期不在前端再次清零后端保留的有效窗口用量`, async () => {
      const before = row('daily', starts.daily)
      const after = { ...before, expires_at: at(70 * day + (mode === 'bulk' ? day / 24 : 0)) }
      const complete = deferredMutation(before, after, mode)
      const admin = mount(AdminSubscriptionsView, { global: { stubs } })
      let user: VueWrapper | undefined
      try {
        await flushPromises()
        await submitExtension(admin, mode, 70)
        complete()
        await flushPromises()
        expect(admin.get('.usage-amount').text()).toMatch(/^1\.125\s*\/\s*10$/)
        expect(admin.getComponent({ name: 'DataTable' }).props('data')).toEqual([after])
        user = mount(UserSubscriptionsView, { global: { stubs } })
        await flushPromises()
        expect(user.text()).toContain('1.125')
        expect(user.text()).toContain('userSubscriptions.resetIn')
      } finally { admin.unmount(); user?.unmount() }
    })

    for (const period of ['daily', 'weekly'] as const) {
      it(`${mode} 延期后有限外层额度允许 ${period} 尾段显示下一次重置`, async () => {
        const days = period === 'daily' ? 1 : 8
        const before = row(period, null)
        before.monthly_limit_usd = 100
        const after = { ...before, expires_at: at(days * day + (mode === 'bulk' ? day / 24 : 0)), [`${period}_window_start`]: starts[period] }
        const complete = deferredMutation(before, after, mode)
        const admin = mount(AdminSubscriptionsView, { global: { stubs } })
        let user: VueWrapper | undefined
        try {
          await flushPromises()
          await submitExtension(admin, mode, days)
          complete()
          await flushPromises()
          expect(admin.get('.reset-info').text()).toContain('admin.subscriptions.resetIn')
          expect(admin.get('.reset-info').text()).not.toContain('admin.subscriptions.quotaEndsIn')
          user = mount(UserSubscriptionsView, { global: { stubs } })
          await flushPromises()
          expect(user.text()).toContain(`"time":"${period === 'daily' ? '12h 0m' : '7d 0h'}"`)
        } finally { admin.unmount(); user?.unmount() }
      })
    }
  }

  it('单条调整到一天的一次性日额度继续显示结束时间，不因外层额度展示再次重置', async () => {
    const before = { ...row('daily', starts.daily), starts_at: at(0), weekly_limit_usd: 100 }
    const after = { ...before, expires_at: at(day) }
    const complete = deferredMutation(before, after, 'single')
    const admin = mount(AdminSubscriptionsView, { global: { stubs } })
    let user: VueWrapper | undefined
    try {
      await flushPromises()
      await submitExtension(admin, 'single', 1)
      complete()
      await flushPromises()
      expect(admin.get('.reset-info').text()).toContain('admin.subscriptions.quotaEndsIn')
      expect(admin.get('.reset-info').text()).not.toContain('admin.subscriptions.resetIn')
      expect(admin.get('.usage-amount').text()).toMatch(/^1\.125\s*\/\s*10$/)
      user = mount(UserSubscriptionsView, { global: { stubs } })
      await flushPromises()
      expect(user.text()).toContain('userSubscriptions.quotaEndsIn')
      expect(user.text()).not.toContain('userSubscriptions.resetIn')
    } finally { admin.unmount(); user?.unmount() }
  })
})
