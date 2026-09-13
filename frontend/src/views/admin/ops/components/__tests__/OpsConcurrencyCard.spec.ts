import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import OpsConcurrencyCard from '../OpsConcurrencyCard.vue'

const mockGetConcurrencyStats = vi.fn()
const mockGetAccountAvailabilityStats = vi.fn()
const mockGetUserConcurrencyStats = vi.fn()

vi.mock('@/api/admin/ops', () => ({
  opsAPI: {
    getConcurrencyStats: (...args: unknown[]) => mockGetConcurrencyStats(...args),
    getAccountAvailabilityStats: (...args: unknown[]) => mockGetAccountAvailabilityStats(...args),
    getUserConcurrencyStats: (...args: unknown[]) => mockGetUserConcurrencyStats(...args),
  },
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

const concurrencyResponse = {
  enabled: true,
  platform: {
    anthropic: {
      platform: 'anthropic',
      current_in_use: 3,
      max_capacity: 10,
      load_percentage: 30,
      waiting_in_queue: 1,
    },
  },
  group: {
    7: {
      group_id: 7,
      group_name: 'Anthropic 主分组',
      platform: 'anthropic',
      current_in_use: 2,
      max_capacity: 8,
      load_percentage: 25,
      waiting_in_queue: 1,
    },
  },
  account: {
    11: {
      account_id: 11,
      account_name: 'Claude 账号 A',
      platform: 'anthropic',
      group_id: 7,
      group_name: 'Anthropic 主分组',
      current_in_use: 2,
      max_capacity: 8,
      load_percentage: 25,
      waiting_in_queue: 0,
    },
  },
}

const availabilityResponse = {
  enabled: true,
  platform: {
    anthropic: {
      platform: 'anthropic',
      total_accounts: 1,
      available_count: 1,
      rate_limit_count: 0,
      error_count: 0,
    },
  },
  group: {
    7: {
      group_id: 7,
      group_name: 'Anthropic 主分组',
      platform: 'anthropic',
      total_accounts: 1,
      available_count: 1,
      rate_limit_count: 0,
      error_count: 0,
    },
  },
  account: {
    11: {
      account_id: 11,
      account_name: 'Claude 账号 A',
      platform: 'anthropic',
      group_id: 7,
      group_name: 'Anthropic 主分组',
      status: 'active',
      is_available: true,
      is_rate_limited: false,
      is_overloaded: false,
      has_error: false,
    },
  },
}

describe('OpsConcurrencyCard', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockGetConcurrencyStats.mockResolvedValue(concurrencyResponse)
    mockGetAccountAvailabilityStats.mockResolvedValue(availabilityResponse)
    mockGetUserConcurrencyStats.mockResolvedValue({
      enabled: true,
      user: {
        21: {
          user_id: 21,
          user_email: 'ops@example.com',
          username: 'ops-user',
          current_in_use: 1,
          max_capacity: 4,
          load_percentage: 25,
          waiting_in_queue: 0,
        },
      },
    })
  })

  it('支持平台、分组、账号、用户四种维度并切换对应数据源', async () => {
    const wrapper = mount(OpsConcurrencyCard, {
      props: {
        refreshToken: 0,
      },
    })
    await flushPromises()

    expect(mockGetConcurrencyStats).toHaveBeenCalledWith('', null)
    expect(wrapper.get('h3').classes()).toContain('whitespace-nowrap')
    expect(wrapper.get('[role="group"]').classes()).toContain('grid-cols-4')
    expect(wrapper.get('[data-test="concurrency-refresh"]').classes()).toEqual(
      expect.arrayContaining(['h-7', 'w-7', 'justify-center', 'p-0'])
    )
    expect(wrapper.text()).toContain('ANTHROPIC')

    await wrapper.get('[data-test="concurrency-dimension-group"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('Anthropic 主分组')

    await wrapper.get('[data-test="concurrency-dimension-account"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('Claude 账号 A')

    await wrapper.get('[data-test="concurrency-dimension-user"]').trigger('click')
    await flushPromises()
    expect(mockGetUserConcurrencyStats).toHaveBeenCalled()
    expect(wrapper.text()).toContain('ops-user')

    await wrapper.get('[data-test="concurrency-dimension-platform"]').trigger('click')
    await flushPromises()
    expect(mockGetConcurrencyStats).toHaveBeenCalledTimes(4)
  })

  it('在固定高度卡片内使用剩余空间滚动，并按分组筛选账号', async () => {
    const wrapper = mount(OpsConcurrencyCard, {
      props: {
        platformFilter: 'anthropic',
        groupIdFilter: 7,
        refreshToken: 0,
      },
    })
    await flushPromises()

    expect(wrapper.classes()).toContain('h-full')
    const scrollRegion = wrapper.find('.custom-scrollbar')
    expect(scrollRegion.classes()).toContain('min-h-0')
    expect(scrollRegion.classes()).toContain('overflow-y-auto')
    expect(wrapper.text()).toContain('Claude 账号 A')
    expect(mockGetConcurrencyStats).toHaveBeenCalledWith('anthropic', 7)
  })

  it('分组视图不会展示筛选分组之外的后端数据', async () => {
    mockGetAccountAvailabilityStats.mockResolvedValue({
      ...availabilityResponse,
      group: {
        ...availabilityResponse.group,
        8: {
          group_id: 8,
          group_name: '越界分组',
          platform: 'anthropic',
          total_accounts: 1,
          available_count: 1,
          rate_limit_count: 0,
          error_count: 0,
        },
      },
    })

    const wrapper = mount(OpsConcurrencyCard, {
      props: {
        platformFilter: 'anthropic',
        groupIdFilter: 7,
        refreshToken: 0,
      },
    })
    await flushPromises()

    await wrapper.get('[data-test="concurrency-dimension-group"]').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('Anthropic 主分组')
    expect(wrapper.text()).not.toContain('越界分组')
  })
})
