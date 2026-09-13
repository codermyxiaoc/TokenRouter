import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import TeamActionMenu from '../TeamActionMenu.vue'
import type { AdminTeam } from '@/api/admin/teams'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const team: AdminTeam = {
  id: 7,
  name: '平台团队',
  status: 'active',
  member_limit: 10,
  member_count: 2,
  created_at: '2026-07-01T00:00:00Z',
  updated_at: '2026-07-01T00:00:00Z',
  owner_user_id: 1,
  owner_email: 'owner@example.com',
}

describe('TeamActionMenu', () => {
  afterEach(() => {
    document.body.innerHTML = ''
  })

  it('提供详情、统计和解散三个更多操作', async () => {
    const wrapper = mount(TeamActionMenu, {
      props: { show: true, team, position: { top: 80, left: 100 } },
      attachTo: document.body,
    })

    const buttons = Array.from(document.body.querySelectorAll('button'))
    expect(document.body.textContent).toContain('team.viewDetails')
    expect(document.body.textContent).toContain('team.viewStatistics')
    expect(document.body.textContent).toContain('team.dissolve')

    buttons.find((button) => button.textContent?.includes('team.viewStatistics'))?.click()
    await wrapper.vm.$nextTick()

    expect(wrapper.emitted('statistics')?.[0]).toEqual([team])
    expect(wrapper.emitted('close')).toHaveLength(1)
    wrapper.unmount()
  })
})
