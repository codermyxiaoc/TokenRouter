import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import type { TicketClosedByRole } from '@/api/tickets'
import TicketBadge from '../TicketBadge.vue'
import zh from '@/i18n/locales/zh/tickets'
import en from '@/i18n/locales/en/tickets'

// 测试直接使用源码词条，需要 full build；生产环境的 runtime-only 别名使用预编译词条。
vi.mock('vue-i18n', () => import('../../../../node_modules/vue-i18n/dist/vue-i18n.mjs'))

function render(value: string, closedByRole?: TicketClosedByRole | null, locale = 'zh', kind: 'status' | 'priority' = 'status') {
  const i18n = createI18n({ legacy: false, locale, messages: { zh, en } })
  return mount(TicketBadge, { props: { kind, value, closedByRole }, global: { plugins: [i18n] } })
}

describe('工单状态与结单方', () => {
  it.each([
    ['zh', 'pending', '待客服回复'],
    ['zh', 'waiting_user', '待用户回复'],
    ['en', 'pending', 'Awaiting support reply'],
    ['en', 'waiting_user', 'Awaiting user reply'],
  ])('回复状态不因查看者身份改变称谓（%s/%s）', (locale, status, label) => {
    const wrapper = render(status, undefined, locale)
    expect(wrapper.text()).toBe(label)
    expect(wrapper.find('[data-testid="ticket-close-role"]').exists()).toBe(false)
  })

  it.each(['completed', 'cancelled'])('完成和撤销明确展示真实用户或客服角色（%s）', status => {
    expect(render(status, 'user').get('[data-testid="ticket-close-role"]').text()).toBe('结单方：用户')
    expect(render(status, 'admin').get('[data-testid="ticket-close-role"]').text()).toBe('结单方：客服')
  })

  it.each([undefined, null, ''] as const)('历史完成或撤销缺少记录时显示未记录（%s）', role => {
    expect(render('completed', role).get('[data-testid="ticket-close-role"]').text()).toBe('结单方：未记录')
    expect(render('cancelled', role).get('[data-testid="ticket-close-role"]').text()).toBe('结单方：未记录')
  })

  it('自动过期显示系统，优先级徽标不显示结单方', () => {
    expect(render('expired').get('[data-testid="ticket-close-role"]').text()).toBe('结单方：系统')
    expect(render('expired', 'system', 'en').get('[data-testid="ticket-close-role"]').text()).toBe('Closed by: System')
    expect(render('normal', 'admin', 'zh', 'priority').find('[data-testid="ticket-close-role"]').exists()).toBe(false)
  })
})
