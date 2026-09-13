import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
const messages = vi.hoisted<Record<string, string>>(() => ({
  'team.inviteActionTitle': '团队邀请',
  'team.inviteActionDescription': '请确认以下邀请信息后选择是否加入团队。',
  'team.invitedTeam': '团队名称',
  'team.inviter': '邀请人',
  'team.inviterEmail': '邀请人邮箱',
  'team.invitationExpiresAt': '邀请有效期',
  'team.decline': '拒绝',
  'team.accept': '接受',
  'common.close': '关闭',
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => messages[key] ?? key,
    }),
  }
})

import TeamInvitationDialog from '../TeamInvitationDialog.vue'

const BaseDialogStub = defineComponent({
  props: {
    show: Boolean,
    title: String,
  },
  emits: ['close'],
  template: '<div v-if="show" data-testid="dialog"><h2>{{ title }}</h2><slot /><slot name="footer" /></div>',
})

const mountDialog = (overrides: Record<string, unknown> = {}) => mount(TeamInvitationDialog, {
  props: {
    show: true,
    loading: false,
    resolving: false,
    error: '',
    preview: {
      team_name: '词元流动',
      inviter_name: '喵窝',
      inviter_email: 'owner@example.com',
      expires_at: '2026-08-04T00:22:40+08:00',
    },
    ...overrides,
  },
  global: {
    stubs: {
      BaseDialog: BaseDialogStub,
      Icon: true,
      LoadingSpinner: true,
    },
  },
})

describe('TeamInvitationDialog', () => {
  it('展示团队、邀请人、邀请邮箱和有效期', () => {
    const wrapper = mountDialog()

    expect(wrapper.get('[data-testid="invitation-details"]').text()).toContain('词元流动')
    expect(wrapper.text()).toContain('喵窝')
    expect(wrapper.text()).toContain('owner@example.com')
    expect(wrapper.text()).toContain('2026')
  })

  it('分别发送拒绝和接受事件', async () => {
    const wrapper = mountDialog()
    const buttons = wrapper.findAll('button')

    await buttons[0].trigger('click')
    await buttons[1].trigger('click')

    expect(wrapper.emitted('resolve')).toEqual([['declined'], ['accepted']])
  })
})
