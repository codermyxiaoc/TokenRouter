import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createI18n, type MessageContext } from 'vue-i18n'
import { createRouter, createMemoryHistory } from 'vue-router'
import type { SubscriptionPlan, UserSubscription } from '@/types'
import SubscriptionsView from '../SubscriptionsView.vue'
import english from '@/i18n/locales/en/misc'

const mockGetMySubscriptions = vi.fn()
const mockGetActiveSubscriptions = vi.fn()
const mockRevokeExhaustedSubscription = vi.fn()

vi.mock('@/api/subscriptions', () => ({
  default: {
    getMySubscriptions: (...args: unknown[]) => mockGetMySubscriptions(...args),
    getActiveSubscriptions: (...args: unknown[]) => mockGetActiveSubscriptions(...args),
    revokeExhaustedSubscription: (...args: unknown[]) => mockRevokeExhaustedSubscription(...args)
  }
}))

function createTestI18n() {
  return createI18n({
    legacy: false,
    locale: 'en',
    messages: {
      en: {
        payment: {
          renewNow: 'Renew now'
        },
        common: {
          today: 'Today',
          tomorrow: 'Tomorrow',
          cancel: 'Cancel',
          confirm: 'Confirm',
          processing: 'Processing...'
        },
        userSubscriptions: {
          noActiveSubscriptions: 'No active subscriptions',
          noActiveSubscriptionsDesc: 'No active subscriptions yet',
          queuedPacks: 'Queued {count}',
          startsAt: 'Starts At',
          expires: 'Expires',
          extendsThrough: 'Extends through {date}',
          daily: 'Daily',
          weekly: 'Weekly',
          monthly: 'Monthly',
          resetIn: ({ named }: MessageContext) => `Reset in ${named('time')}`,
          quotaEndsIn: ({ named }: MessageContext) => `Ends in ${named('time')}`,
          // 测试环境使用 runtime-only i18n，消息函数验证实际计数而非只检查翻译键。
          resetCount: ({ named }: MessageContext) => english.userSubscriptions.resetCount.replace('{count}', String(named('count'))),
          resetCountHint: () => english.userSubscriptions.resetCountHint,
          quotaNoFurtherResetHint: () => english.userSubscriptions.quotaNoFurtherResetHint,
          oneTimeQuotaHint: () => english.userSubscriptions.oneTimeQuotaHint,
          unlimited: 'Unlimited',
          unlimitedDesc: 'Unlimited usage',
          pendingOnly: 'Pending only',
          failedToLoad: 'Failed to load subscriptions',
          daysRemaining: '{days} days remaining',
          windowNotActive: 'Window not active',
          revoke: 'Revoke plan',
          revokeTitle: 'Revoke exhausted plan',
          revokeConfirmWithReplacement: 'Queued pack will start immediately.',
          revokeConfirmWithoutReplacement: 'No queued pack will replace this plan.',
          revokeSuccessWithReplacement: 'Plan revoked and {count} API key(s) rebound.',
          revokeSuccess: 'Plan revoked successfully.',
          revokeFailed: 'Failed to revoke plan.',
          groupAccess: 'Accessible groups',
          allGroups: 'All groups',
          restrictedGroups: 'Restricted groups',
          status: {
            active: 'Active',
            pending: 'Pending',
            expired: 'Expired'
          }
        }
      }
    }
  })
}

async function mountView() {
  const pinia = createPinia()
  setActivePinia(pinia)
  const i18n = createTestI18n()
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/', component: { template: '<div />' } }]
  })
  await router.push('/')
  await router.isReady()

  return mount(SubscriptionsView, {
    global: {
      plugins: [pinia, i18n, router],
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        Icon: { template: '<span />' },
        ConfirmDialog: {
          props: ['show', 'title', 'message', 'confirmText', 'cancelText', 'danger', 'loading'],
          emits: ['confirm', 'cancel'],
          template: `
            <div v-if="show" data-testid="revoke-dialog">
              <p data-testid="revoke-dialog-message">{{ message }}</p>
              <button data-testid="revoke-dialog-confirm" :disabled="loading" @click="$emit('confirm')">{{ confirmText }}</button>
              <button data-testid="revoke-dialog-cancel" :disabled="loading" @click="$emit('cancel')">{{ cancelText }}</button>
            </div>
          `
        }
      }
    }
  })
}

// 构造完整订阅，测试只覆盖接口已解析的分组展示倍率。
function subscriptionWithGroups(plan: Partial<SubscriptionPlan> = {}): UserSubscription {
  const startsAt = new Date(Date.now() - 60 * 60 * 1000).toISOString()
  const planId = plan.id ?? 101
  return {
    id: planId,
    user_id: 7,
    plan_id: planId,
    starts_at: startsAt,
    expires_at: new Date(Date.now() + 30 * 24 * 60 * 60 * 1000).toISOString(),
    status: 'active',
    daily_limit_usd: null,
    weekly_limit_usd: null,
    monthly_limit_usd: null,
    daily_usage_usd: 0,
    weekly_usage_usd: 0,
    monthly_usage_usd: 0,
    daily_window_start: null,
    weekly_window_start: null,
    monthly_window_start: null,
    created_at: startsAt,
    updated_at: startsAt,
    plan: {
      id: planId,
      name: 'Group access plan',
      description: '',
      price: 10,
      features: [],
      validity_days: 30,
      validity_unit: 'day',
      for_sale: true,
      sort_order: 0,
      groups_restricted: true,
      ...plan
    }
  }
}

describe('SubscriptionsView', () => {
  beforeEach(() => {
    mockGetMySubscriptions.mockReset()
    mockGetActiveSubscriptions.mockReset().mockResolvedValue([])
    mockRevokeExhaustedSubscription.mockReset()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('shows the server reset count for each configured window even before it is started', async () => {
    mockGetMySubscriptions.mockResolvedValue([{
      ...subscriptionWithGroups(),
      daily_limit_usd: 10, weekly_limit_usd: 60, monthly_limit_usd: 150,
      daily_reset_count: 17, weekly_reset_count: 3, monthly_reset_count: 2
    }])
    const wrapper = await mountView()
    await flushPromises()

    expect(wrapper.get('[data-testid="quota-reset-count-daily"]').text()).toBe('Reset 17 times')
    expect(wrapper.get('[data-testid="quota-reset-count-weekly"]').text()).toBe('Reset 3 times')
    expect(wrapper.get('[data-testid="quota-reset-count-monthly"]').text()).toBe('Reset 2 times')
    expect(wrapper.get('[data-testid="quota-reset-count-daily"]').attributes('title')).toContain('even without usage')
    wrapper.unmount()
  })

  it('defaults legacy missing reset counts to zero without inferring elapsed windows', async () => {
    mockGetMySubscriptions.mockResolvedValue([{
      ...subscriptionWithGroups(),
      starts_at: new Date(Date.now() - 90 * 86400000).toISOString(),
      daily_limit_usd: 10,
      daily_window_start: new Date(Date.now() - 24 * 86400000).toISOString()
    }])
    const wrapper = await mountView()
    await flushPromises()

    expect(wrapper.get('[data-testid="quota-reset-count-daily"]').text()).toBe('Reset 0 times')
    expect(wrapper.find('[data-testid="quota-reset-count-weekly"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="quota-reset-count-monthly"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('switches to the new term count when the backend activates a renewed subscription', async () => {
    const active = { ...subscriptionWithGroups(), daily_limit_usd: 10, daily_reset_count: 17 }
    const next: UserSubscription = {
      ...active, id: active.id + 1, status: 'pending', daily_reset_count: 0,
      starts_at: active.expires_at,
      expires_at: new Date(new Date(active.expires_at).getTime() + 30 * 86400000).toISOString()
    }
    mockGetMySubscriptions.mockResolvedValueOnce([active, next])
      .mockResolvedValueOnce([{ ...active, status: 'expired' }, { ...next, status: 'active' }])
    const before = await mountView()
    await flushPromises()
    expect(before.findAll('[data-testid="quota-reset-count-daily"]')).toHaveLength(1)
    expect(before.get('[data-testid="quota-reset-count-daily"]').text()).toBe('Reset 17 times')
    before.unmount()

    const after = await mountView()
    await flushPromises()
    expect(after.findAll('[data-testid="quota-reset-count-daily"]')).toHaveLength(1)
    expect(after.get('[data-testid="quota-reset-count-daily"]').text()).toBe('Reset 0 times')
    after.unmount()
  })

  it('preserves server counts after an administrator extends validity and restarts an inactive window', async () => {
    const original = {
      ...subscriptionWithGroups(), starts_at: new Date(Date.now() - 40 * 86400000).toISOString(),
      expires_at: new Date(Date.now() + 3600000).toISOString(),
      daily_limit_usd: 10, daily_reset_count: 8
    }
    mockGetMySubscriptions.mockResolvedValueOnce([original]).mockResolvedValueOnce([{
      ...original,
      expires_at: new Date(Date.now() + 30 * 86400000).toISOString(),
      daily_window_start: new Date().toISOString()
    }])
    const before = await mountView()
    await flushPromises()
    expect(before.get('[data-testid="quota-reset-count-daily"]').text()).toBe('Reset 8 times')
    before.unmount()

    const after = await mountView()
    await flushPromises()
    expect(after.get('[data-testid="quota-reset-count-daily"]').text()).toBe('Reset 8 times')
    expect(after.text()).toContain('Reset in')
    expect(after.find('[data-testid="quota-window-explanation-daily"]').exists()).toBe(false)
    after.unmount()
  })

  it('allows the final daily reset before expiry without requiring an outer quota or a complete remaining day', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-09-23T12:00:00+08:00'))
    mockGetMySubscriptions.mockResolvedValue([{
      ...subscriptionWithGroups(), starts_at: '2026-09-01T00:00:00+08:00',
      expires_at: '2026-09-24T01:00:00+08:00', daily_limit_usd: 10,
      daily_window_start: '2026-09-23T00:00:00+08:00', daily_reset_count: 21
    }])
    const wrapper = await mountView()
    await flushPromises()
    expect(wrapper.text()).toContain('Reset in 12h 0m')
    expect(wrapper.find('[data-testid="quota-window-explanation-daily"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('Ends in')
    wrapper.unmount()
  })

  it('explains why the next daily reset at subscription expiry cannot happen', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-09-23T12:00:00+08:00'))
    mockGetMySubscriptions.mockResolvedValue([{
      ...subscriptionWithGroups(), starts_at: '2026-09-01T00:00:00+08:00',
      expires_at: '2026-09-24T00:00:00+08:00', daily_limit_usd: 10, monthly_limit_usd: 200,
      daily_window_start: '2026-09-23T00:00:00+08:00'
    }])
    const wrapper = await mountView()
    await flushPromises()
    expect(wrapper.text()).toContain('Ends in')
    expect(wrapper.get('[data-testid="quota-window-explanation-daily"]').text()).toBe(english.userSubscriptions.quotaNoFurtherResetHint)
    expect(wrapper.text()).not.toContain('Reset in')
    wrapper.unmount()
  })

  it('describes a one-day plan as one-time quota even when a finite outer quota exists', async () => {
    vi.useFakeTimers({ toFake: ['Date'] })
    vi.setSystemTime(new Date('2026-09-23T12:00:00+08:00'))
    mockGetMySubscriptions.mockResolvedValue([{
      ...subscriptionWithGroups(), starts_at: '2026-09-23T00:00:00+08:00',
      expires_at: '2026-09-24T00:00:00+08:00', daily_limit_usd: 10, monthly_limit_usd: 200,
      daily_window_start: '2026-09-23T00:00:00+08:00'
    }])
    const wrapper = await mountView()
    await flushPromises()
    expect(wrapper.get('[data-testid="quota-window-explanation-daily"]').text()).toBe(english.userSubscriptions.oneTimeQuotaHint)
    expect(wrapper.text()).not.toContain('Reset in')
    expect(wrapper.get('[data-testid="quota-reset-count-daily"]').text()).toBe('Reset 0 times')
    wrapper.unmount()
  })

  it('shows resolved group rates independently for each plan, including a free default group', async () => {
    mockGetMySubscriptions.mockResolvedValue([
      subscriptionWithGroups({
        id: 101,
        name: 'Plan Alpha',
        group_rate_multipliers: { 42: 0.65 },
        applicable_groups: [
          { id: 42, name: 'Shared group', platform: 'openai', rate_multiplier: 0.65 },
          { id: 43, name: 'Default group', platform: 'anthropic', rate_multiplier: 2.5 },
          { id: 44, name: 'Free group', platform: 'gemini', rate_multiplier: 0 }
        ]
      }),
      subscriptionWithGroups({
        id: 202,
        name: 'Plan Beta',
        group_rate_multipliers: { 42: 1.75 },
        applicable_groups: [{ id: 42, name: 'Shared group', platform: 'openai', rate_multiplier: 1.75 }]
      })
    ])

    const wrapper = await mountView()
    await flushPromises()

    expect(wrapper.findAll('[title="Shared group"]').map((group) => (
      group.findAll('span').map((part) => part.text())
    ))).toEqual([
      ['Shared group', '0.65x'],
      ['Shared group', '1.75x']
    ])
    expect(wrapper.get('[title="Default group"]').text()).toMatch(/^Default group\s*2\.5x$/)
    expect(wrapper.get('[title="Free group"]').text()).toMatch(/^Free group\s*0x$/)
  })

  it('keeps the full long group name available alongside its rate', async () => {
    const longName = '用于跨平台模型访问的超长分组名称'.repeat(8)
    mockGetMySubscriptions.mockResolvedValue([
      subscriptionWithGroups({
        applicable_groups: [{ id: 42, name: longName, rate_multiplier: 0.75 }]
      })
    ])

    const wrapper = await mountView()
    await flushPromises()

    const group = wrapper.get(`[title="${longName}"]`)
    expect(group.findAll('span').map((part) => part.text())).toEqual([longName, '0.75x'])
  })

  it('keeps legacy groups without an invented rate and preserves unnamed group fallbacks', async () => {
    mockGetMySubscriptions.mockResolvedValue([
      subscriptionWithGroups({
        applicable_groups: [
          { id: 41, name: 'Legacy group' },
          { id: 42, name: '', rate_multiplier: 1.25 }
        ]
      })
    ])

    const wrapper = await mountView()
    await flushPromises()

    expect(wrapper.get('[title="Legacy group"]').text()).toBe('Legacy group')
    expect(wrapper.get('[title="#42"]').text()).toMatch(/^#42\s*1\.25x$/)
  })

  it('groups same-plan active and pending subscriptions into one chain card', async () => {
    mockGetMySubscriptions.mockResolvedValue([
      {
        id: 1,
        user_id: 7,
        plan_id: 101,
        starts_at: '2026-04-01T00:00:00Z',
        expires_at: '2026-05-01T00:00:00Z',
        status: 'active',
        daily_limit_usd: 10,
        weekly_limit_usd: null,
        monthly_limit_usd: null,
        daily_usage_usd: 3,
        weekly_usage_usd: 0,
        monthly_usage_usd: 0,
        daily_window_start: '2026-04-20T00:00:00Z',
        weekly_window_start: null,
        monthly_window_start: null,
        created_at: '2026-04-01T00:00:00Z',
        updated_at: '2026-04-01T00:00:00Z',
        plan: {
          id: 101,
          name: 'Plan Alpha',
          description: 'Alpha description',
          price: 10,
          features: [],
          validity_days: 30,
          validity_unit: 'day',
          daily_limit_usd: 10,
          weekly_limit_usd: null,
          monthly_limit_usd: null,
          for_sale: true,
          sort_order: 0
        }
      },
      {
        id: 2,
        user_id: 7,
        plan_id: 101,
        starts_at: '2026-05-01T00:00:00Z',
        expires_at: '2026-05-31T00:00:00Z',
        status: 'pending',
        daily_limit_usd: 10,
        weekly_limit_usd: null,
        monthly_limit_usd: null,
        daily_usage_usd: 0,
        weekly_usage_usd: 0,
        monthly_usage_usd: 0,
        daily_window_start: null,
        weekly_window_start: null,
        monthly_window_start: null,
        created_at: '2026-04-10T00:00:00Z',
        updated_at: '2026-04-10T00:00:00Z',
        plan: {
          id: 101,
          name: 'Plan Alpha',
          description: 'Alpha description',
          price: 10,
          features: [],
          validity_days: 30,
          validity_unit: 'day',
          daily_limit_usd: 10,
          weekly_limit_usd: null,
          monthly_limit_usd: null,
          for_sale: true,
          sort_order: 0
        }
      },
      {
        id: 3,
        user_id: 7,
        plan_id: 202,
        starts_at: '2026-04-15T00:00:00Z',
        expires_at: '2026-04-25T00:00:00Z',
        status: 'active',
        daily_limit_usd: null,
        weekly_limit_usd: 40,
        monthly_limit_usd: null,
        daily_usage_usd: 0,
        weekly_usage_usd: 8,
        monthly_usage_usd: 0,
        daily_window_start: null,
        weekly_window_start: '2026-04-18T00:00:00Z',
        monthly_window_start: null,
        created_at: '2026-04-15T00:00:00Z',
        updated_at: '2026-04-15T00:00:00Z',
        plan: {
          id: 202,
          name: 'Plan Beta',
          description: 'Beta description',
          price: 20,
          features: [],
          validity_days: 10,
          validity_unit: 'day',
          daily_limit_usd: null,
          weekly_limit_usd: 40,
          monthly_limit_usd: null,
          for_sale: true,
          sort_order: 0
        }
      }
    ])

    const wrapper = await mountView()
    await flushPromises()

    const text = wrapper.text()
    expect(mockGetMySubscriptions).toHaveBeenCalledTimes(1)
    expect(text.match(/Plan Alpha/g)?.length).toBe(1)
    expect(text.match(/Plan Beta/g)?.length).toBe(1)
    expect(text).toMatch(/Queued 1|userSubscriptions\.queuedPacks/)
  })

  it('shows revoke only when the highest configured quota is exhausted', async () => {
    const now = Date.now()
    const subscription = {
        id: 9,
        user_id: 7,
        plan_id: 101,
        starts_at: new Date(now - 60 * 60 * 1000).toISOString(),
        expires_at: new Date(now + 24 * 60 * 60 * 1000).toISOString(),
        status: 'active',
        daily_limit_usd: 1,
        daily_usage_usd: 1,
        weekly_limit_usd: 10,
        weekly_usage_usd: 10,
        monthly_limit_usd: 100,
        monthly_usage_usd: 99,
        daily_window_start: new Date(now - 30 * 60 * 1000).toISOString(),
        weekly_window_start: new Date(now - 30 * 60 * 1000).toISOString(),
        monthly_window_start: new Date(now - 30 * 60 * 1000).toISOString(),
        created_at: new Date(now - 60 * 60 * 1000).toISOString(),
        updated_at: new Date(now - 60 * 60 * 1000).toISOString(),
        plan: { id: 101, name: 'Plan Alpha', description: '', price: 10, features: [], validity_days: 30, validity_unit: 'day', daily_limit_usd: 1, weekly_limit_usd: 10, monthly_limit_usd: 100, for_sale: true, sort_order: 0 }
    }
    mockGetMySubscriptions.mockResolvedValue([subscription])

    const wrapper = await mountView()
    await flushPromises()
    expect(wrapper.find('[data-testid="revoke-subscription"]').exists()).toBe(false)

    await wrapper.unmount()
    mockGetMySubscriptions.mockResolvedValue([{ ...subscription, monthly_usage_usd: 100 }])
    const exhaustedWrapper = await mountView()
    await flushPromises()
    expect(exhaustedWrapper.find('[data-testid="revoke-subscription"]').exists()).toBe(true)
  })

  it('requires confirmation and prevents duplicate revoke submissions', async () => {
    const now = Date.now()
    mockGetMySubscriptions.mockResolvedValue([
      {
        id: 12,
        user_id: 7,
        plan_id: 303,
        starts_at: new Date(now - 60 * 60 * 1000).toISOString(),
        expires_at: new Date(now + 24 * 60 * 60 * 1000).toISOString(),
        status: 'active',
        daily_limit_usd: null,
        daily_usage_usd: 0,
        weekly_limit_usd: null,
        weekly_usage_usd: 0,
        monthly_limit_usd: 50,
        monthly_usage_usd: 50,
        daily_window_start: null,
        weekly_window_start: null,
        monthly_window_start: new Date(now - 30 * 60 * 1000).toISOString(),
        created_at: new Date(now - 60 * 60 * 1000).toISOString(),
        updated_at: new Date(now - 60 * 60 * 1000).toISOString(),
        plan: { id: 303, name: 'Plan Gamma', description: '', price: 10, features: [], validity_days: 30, validity_unit: 'day', daily_limit_usd: null, weekly_limit_usd: null, monthly_limit_usd: 50, for_sale: true, sort_order: 0 }
      }
    ])
    let resolveRevoke!: (value: { revoked_subscription_id: number; replacement_subscription_id: number | null; rebound_api_key_count: number }) => void
    mockRevokeExhaustedSubscription.mockReturnValue(new Promise((resolve) => { resolveRevoke = resolve }))

    const wrapper = await mountView()
    await flushPromises()
    await wrapper.get('[data-testid="revoke-subscription"]').trigger('click')
    const dialog = wrapper.get('[data-testid="revoke-dialog"]')
    expect(dialog.text()).toContain('userSubscriptions.revokeConfirmWithoutReplacement')

    await wrapper.get('[data-testid="revoke-dialog-confirm"]').trigger('click')
    await wrapper.vm.$nextTick()
    expect(mockRevokeExhaustedSubscription).toHaveBeenCalledTimes(1)
    expect(mockRevokeExhaustedSubscription).toHaveBeenCalledWith(12)
    expect(wrapper.get('[data-testid="revoke-dialog-confirm"]').attributes('disabled')).toBeDefined()

    await wrapper.get('[data-testid="revoke-dialog-confirm"]').trigger('click')
    expect(mockRevokeExhaustedSubscription).toHaveBeenCalledTimes(1)
    resolveRevoke({ revoked_subscription_id: 12, replacement_subscription_id: null, rebound_api_key_count: 0 })
    await flushPromises()
  })
})
