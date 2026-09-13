import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import TeamView from '../TeamView.vue'

const {
  currentTeam,
  getMembers,
  getInvitations,
  getTeamKeys,
  startTeamGuide,
} = vi.hoisted(() => ({
  currentTeam: vi.fn(),
  getMembers: vi.fn(),
  getInvitations: vi.fn(),
  getTeamKeys: vi.fn(),
  startTeamGuide: vi.fn(),
}))

vi.mock('@/api/team', () => ({
  teamAPI: {
    current: currentTeam,
    members: getMembers,
    invitations: getInvitations,
    keys: getTeamKeys,
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
  }),
}))

vi.mock('@/stores/onboarding', () => ({
  useOnboardingStore: () => ({ startTeamGuide }),
}))

vi.mock('@/composables/useBalanceDisplay', () => ({
  useBalanceDisplay: () => ({
    balanceUnitSymbol: '$',
    hasCustomBalanceIcon: false,
  }),
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
  useRouter: () => ({ replace: vi.fn() }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

const owner = {
  id: 1,
  team_id: 1,
  user_id: 1,
  email: 'owner@example.com',
  username: 'owner',
  role: 'owner',
  daily_limit_usd: 0,
  weekly_limit_usd: 0,
  monthly_limit_usd: 0,
  daily_usage_usd: 0,
  weekly_usage_usd: 0,
  monthly_usage_usd: 0,
  joined_at: '2026-07-29T00:00:00Z',
  last_active_at: null,
}

const teamContext = {
  team: {
    id: 1,
    name: 'Test team',
    status: 'active',
    member_limit: 10,
    default_daily_limit_usd: 0,
    default_weekly_limit_usd: 0,
    default_monthly_limit_usd: 0,
    member_count: 0,
    created_at: '2026-07-29T00:00:00Z',
    updated_at: '2026-07-29T00:00:00Z',
  },
  membership: owner,
  owner,
}

// 仅渲染团队页自身内容，避免布局中的全局导览控制器影响入口行为测试。
const AppLayoutStub = {
  template: '<div><slot /></div>',
}

const mountTeamView = async () => {
  const wrapper = mount(TeamView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        BaseDialog: true,
        ConfirmDialog: true,
        LoadingSpinner: true,
        Icon: true,
        TeamInvitationDialog: true,
        TotpStepUpDialog: true,
        BalanceAmount: true,
        BalanceIcon: true,
      },
    },
  })
  await flushPromises()
  return wrapper
}

describe('TeamView 功能引导', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    currentTeam.mockResolvedValue(teamContext)
    getMembers.mockResolvedValue([owner])
    getInvitations.mockResolvedValue([])
    getTeamKeys.mockResolvedValue([])
  })

  it.each([
    'team.keys',
    'team.settings',
  ])('从%s页签启动时先恢复概览页', async (tabLabel) => {
    const wrapper = await mountTeamView()
    const tab = wrapper.findAll('nav button').find((button) => button.text() === tabLabel)
    expect(tab).toBeDefined()

    await tab!.trigger('click')
    expect(wrapper.find('[data-tour="team-members"]').exists()).toBe(false)

    const guideButton = wrapper.findAll('button').find((button) => button.text().includes('team.guideButton'))
    expect(guideButton).toBeDefined()
    await guideButton!.trigger('click')

    expect(wrapper.find('[data-tour="team-members"]').exists()).toBe(true)
    expect(startTeamGuide).toHaveBeenCalledWith({ isOwner: true, hasTeam: true })
    wrapper.unmount()
  })
})
