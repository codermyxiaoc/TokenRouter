import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import type { UserDashboardStats as UserDashboardStatsType } from '@/api/usage'
import UserDashboardStats from '../UserDashboardStats.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

vi.mock('@/composables/useBalanceDisplay', () => ({
  useBalanceDisplay: () => ({
    formatBalanceAmount: (value: number) => `🍥${value.toFixed(4)}`,
  }),
}))

const stats: UserDashboardStatsType = {
  total_api_keys: 11,
  active_api_keys: 10,
  total_requests: 191,
  total_input_tokens: 1_500_000,
  total_output_tokens: 100_000,
  total_cache_creation_tokens: 0,
  total_cache_read_tokens: 1_000_000,
  total_tokens: 2_600_000,
  total_cost: 6.9529,
  total_actual_cost: 8.8717,
  today_requests: 0,
  today_input_tokens: 0,
  today_output_tokens: 0,
  today_cache_creation_tokens: 0,
  today_cache_read_tokens: 0,
  today_tokens: 0,
  today_cost: 3.4567,
  today_actual_cost: 1.2345,
  average_duration_ms: 4810,
  rpm: 0,
  tpm: 0,
}

describe('UserDashboardStats', () => {
  it('only shows the final billed amount in the user cost card', () => {
    const wrapper = mount(UserDashboardStats, {
      props: {
        stats,
        balance: 8215.03,
        isSimple: false,
      },
      global: {
        stubs: {
          BalanceIcon: true,
          Icon: true,
        },
      },
    })

    const costCard = wrapper.get('[data-testid="user-dashboard-cost"]')
    expect(costCard.text()).toContain('🍥1.2345')
    expect(costCard.text()).toContain('🍥8.8717')
    expect(costCard.text()).not.toContain('$')
    expect(costCard.text()).not.toContain('3.4567')
    expect(costCard.text()).not.toContain('6.9529')
  })
})
