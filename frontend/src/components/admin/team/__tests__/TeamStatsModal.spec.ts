import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import TeamStatsModal from '../TeamStatsModal.vue'
import type { AdminTeam } from '@/api/admin/teams'
import type { TeamUsageSummary } from '@/api/team'

const { members, usage, showError } = vi.hoisted(() => ({
  members: vi.fn(),
  usage: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: { teams: { members, usage } },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const team: AdminTeam = {
  id: 9,
  name: '统计团队',
  status: 'active',
  member_limit: 10,
  member_count: 1,
  created_at: '2026-07-01T00:00:00Z',
  updated_at: '2026-07-01T00:00:00Z',
  owner_user_id: 1,
  owner_email: 'owner@example.com',
}

const summary = (cost: number): TeamUsageSummary => ({
  actual_cost: cost,
  request_count: Math.round(cost * 10),
  input_tokens: 100,
  output_tokens: 20,
  daily: [{ date: '2026-07-27', actual_cost: cost, request_count: 1 }],
})

describe('TeamStatsModal', () => {
  beforeEach(() => {
    members.mockReset()
    usage.mockReset()
    showError.mockReset()
    members.mockResolvedValue([
      { user_id: 1, username: 'Owner', email: 'owner@example.com' },
      { user_id: 2, username: '', email: 'member@example.com' },
    ])
    usage.mockImplementation((_teamID: number, query?: { member_id?: number }) =>
      Promise.resolve(summary(query?.member_id ? query.member_id / 10 : 1)),
    )
  })

  it('加载团队汇总并为每名成员请求独立统计', async () => {
    const wrapper = mount(TeamStatsModal, {
      props: { show: true, team },
      global: {
        stubs: {
          BaseDialog: {
            props: ['show', 'title'],
            template: '<section v-if="show"><slot /><slot name="footer" /></section>',
          },
          BalanceAmount: { props: ['amount'], template: '<span>{{ amount }}</span>' },
          LoadingSpinner: true,
          TeamMemberUsageCharts: {
            props: ['series', 'loading'],
            template: '<div data-test="member-series">{{ series.map(item => item.label).join(",") }}</div>',
          },
        },
      },
    })

    await flushPromises()

    expect(members).toHaveBeenCalledWith(9)
    expect(usage).toHaveBeenCalledWith(9)
    expect(usage).toHaveBeenCalledWith(9, { member_id: 1 })
    expect(usage).toHaveBeenCalledWith(9, { member_id: 2 })
    expect(wrapper.get('[data-test="member-series"]').text()).toBe('Owner,member@example.com')
  })
})
