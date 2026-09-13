import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'

import { formatDateTimeToMinute } from '@/utils/format'
import SubscriptionProgressMini from '../SubscriptionProgressMini.vue'

const mockGetActiveSubscriptions = vi.fn()

vi.mock('@/api/subscriptions', () => ({
  default: {
    getActiveSubscriptions: (...args: unknown[]) => mockGetActiveSubscriptions(...args)
  }
}))

vi.mock('@/composables/useBalanceDisplay', () => ({
  useBalanceDisplay: () => ({
    formatBalanceAmount: (value: number) => value.toFixed(2)
  })
}))

function createTestI18n() {
  return createI18n({
    legacy: false,
    locale: 'en',
    messages: {
      en: {
        subscriptionProgress: {
          title: () => 'My Subscriptions',
          viewDetails: () => 'View subscription details',
          activeCount: ({ named }: { named: (key: string) => unknown }) => `${named('count')} active subscription(s)`,
          daily: () => 'Daily',
          weekly: () => 'Weekly',
          monthly: () => 'Monthly',
          expiresAt: ({ named }: { named: (key: string) => unknown }) => `Expires ${named('time')}`,
          viewAll: () => 'View all subscriptions',
          unlimited: () => 'Unlimited'
        }
      }
    }
  })
}

describe('SubscriptionProgressMini', () => {
  beforeEach(() => {
    mockGetActiveSubscriptions.mockReset()
  })

  it('在订阅弹层展示精确到分钟的本地到期时间', async () => {
    // 使用不带时区偏移的本地时间，避免测试环境时区改变期望值。
    const expiresAt = '2026-08-08T10:33:45'
    mockGetActiveSubscriptions.mockResolvedValue([
      {
        id: 1,
        user_id: 7,
        plan_id: 101,
        starts_at: '2026-07-09T10:33:45',
        expires_at: expiresAt,
        status: 'active',
        daily_limit_usd: 200,
        weekly_limit_usd: 1000,
        monthly_limit_usd: 1000,
        daily_usage_usd: 200,
        weekly_usage_usd: 548.99,
        monthly_usage_usd: 860.98,
        daily_window_start: '2026-08-07T00:00:00',
        weekly_window_start: '2026-08-03T00:00:00',
        monthly_window_start: '2026-07-09T00:00:00',
        created_at: '2026-07-09T10:33:45',
        updated_at: '2026-08-08T00:16:41',
        plan: { name: 'Lite+' }
      }
    ])

    const wrapper = mount(SubscriptionProgressMini, {
      global: {
        plugins: [createPinia(), createTestI18n()],
        stubs: {
          Icon: { template: '<span />' },
          RouterLink: { template: '<a><slot /></a>' }
        }
      }
    })
    await flushPromises()
    await wrapper.get('button').trigger('click')

    expect(wrapper.text()).toContain(`Expires ${formatDateTimeToMinute(expiresAt)}`)
    expect(wrapper.text()).not.toContain('Expires tomorrow')
    wrapper.unmount()
  })
})
