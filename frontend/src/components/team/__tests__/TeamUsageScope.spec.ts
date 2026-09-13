import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import TeamUsageScope from '../TeamUsageScope.vue'

const { getCurrentTeam, getMembers, getKeys, getUsage, getUsageLogs, showError } = vi.hoisted(() => ({
  getCurrentTeam: vi.fn(),
  getMembers: vi.fn(),
  getKeys: vi.fn(),
  getUsage: vi.fn(),
  getUsageLogs: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/api/team', () => ({
  teamAPI: {
    current: getCurrentTeam,
    members: getMembers,
    keys: getKeys,
    usage: getUsage,
    usageLogs: getUsageLogs,
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  const messages: Record<string, string> = {
    'team.totalCost': 'Team spend',
    'team.requests': 'Requests',
    'team.inputTokens': 'Input tokens',
    'team.outputTokens': 'Output tokens',
    'team.allMembers': 'All members',
    'team.allKeys': 'All team keys',
    'team.usageDetails': 'Usage details',
    'team.keyOwner': 'Member',
    'team.keys': 'Team keys',
    'team.model': 'Model',
    'team.tokens': 'Tokens',
    'team.cost': 'Cost',
    'team.time': 'Time',
    'team.noUsage': 'No team usage yet',
  }
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => messages[key] ?? key }),
  }
})

// 测试数据同时覆盖汇总趋势与单条明细，便于验证两个页面模式的职责边界。
const summary = {
  actual_cost: 0.5,
  request_count: 2,
  input_tokens: 100,
  output_tokens: 20,
  daily: [{ date: '2026-07-27', actual_cost: 0.5, request_count: 2 }],
}

const mountScope = async (mode: 'dashboard' | 'usage') => {
  const wrapper = mount(TeamUsageScope, {
    props: { mode },
    global: {
      stubs: {
        LoadingSpinner: true,
        Select: true,
        Icon: true,
        BalanceAmount: {
          props: ['amount'],
          template: '<span>{{ amount }}</span>',
        },
        RouterLink: true,
      },
    },
  })
  await flushPromises()
  return wrapper
}

describe('TeamUsageScope', () => {
  beforeEach(() => {
    getCurrentTeam.mockReset()
    getMembers.mockReset()
    getKeys.mockReset()
    getUsage.mockReset()
    getUsageLogs.mockReset()
    showError.mockReset()

    getCurrentTeam.mockResolvedValue({
      team: { id: 1, name: 'Team', status: 'active', member_limit: 10, member_count: 0 },
      membership: { role: 'owner' },
    })
    getMembers.mockResolvedValue([])
    getKeys.mockResolvedValue([])
    getUsage.mockResolvedValue(summary)
    getUsageLogs.mockResolvedValue({
      items: [{ id: 1, actor_email: 'owner@example.com', api_key_name: 'team-key', model: 'gpt-5.6', input_tokens: 100, output_tokens: 20, actual_cost: 0.5, created_at: '2026-07-27T00:00:00Z' }],
      total: 1,
    })
  })

  it('keeps the dashboard focused on summary and trend data', async () => {
    const wrapper = await mountScope('dashboard')

    expect(getUsage).toHaveBeenCalledOnce()
    expect(getUsageLogs).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('Team spend')
    expect(wrapper.text()).not.toContain('Usage details')
  })

  it('keeps the detailed table on the usage page', async () => {
    const wrapper = await mountScope('usage')

    expect(getUsageLogs).toHaveBeenCalledOnce()
    expect(wrapper.text()).toContain('gpt-5.6')
  })
})
