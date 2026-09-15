import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import type { Ticket } from '@/api/tickets'

const { list, config, close, ticketAPI } = vi.hoisted(() => ({ list: vi.fn(), config: vi.fn(), close: vi.fn(), ticketAPI: vi.fn() }))
const syncTicketModuleEnabled = vi.hoisted(() => vi.fn().mockResolvedValue(undefined))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ syncTicketModuleEnabled }) }))
vi.mock('@/api/client', () => ({ apiClient: {} }))
vi.mock('@/api/tickets', async original => ({ ...await original<typeof import('@/api/tickets')>(), ticketAPI }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, params?: { role?: string }) => key === 'tickets.closedBy' ? `${key}:${params?.role}` : key }) }))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }), RouterLink: { template: '<a><slot /></a>' } }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><h1>工单管理</h1><slot name="page-heading-actions" /><slot /></main>' } }))
import TicketsView from '../TicketsView.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Select from '@/components/common/Select.vue'

const fixture: Ticket = { id: 42, user_id: 7, user_name: '用户', user_email: 'user@example.test', title: '需要帮助', content: '问题描述', type: 'consultation', status: 'pending', priority: 'normal', created_at: '2026-09-13T00:00:00Z', updated_at: '2026-09-13T00:00:00Z' }
function render(admin = false, realBadge = false) { return mount(TicketsView, { props: { admin }, global: { stubs: { TicketBadge: !realBadge, CreateTicketDialog: true, ConfirmDialog: true, Pagination: true, Select: true } } }) }

describe('工单列表', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    ticketAPI.mockReturnValue({ list, config, close })
    list.mockResolvedValue({ items: [fixture, { ...fixture, id: 43, status: 'completed' }], total: 2, page: 1, page_size: 20, pages: 1 })
    config.mockResolvedValue({ enabled: true, max_open_tickets: 3 })
    close.mockResolvedValue({ ...fixture, status: 'completed' })
  })

  it('标题仅由公共布局呈现，筛选仅包含咨询、财务、技术', async () => {
    const wrapper = render()
    await flushPromises()
    expect(wrapper.findAll('h1')).toHaveLength(1)
    expect(wrapper.findAll('button').some(button => button.text() === 'tickets.create')).toBe(true)
    expect(wrapper.findAllComponents(Select)[0].props('options').map((option: { value: string }) => option.value)).toEqual(['', 'consultation', 'financial', 'technical'])
  })

  it.each([false, true])('用户和管理员列表均先确认再关单，关闭中的请求不会重复提交（admin=%s）', async admin => {
    let resolve!: (value: Ticket) => void
    close.mockReturnValue(new Promise(done => { resolve = done }))
    const wrapper = render(admin)
    await flushPromises()
    const rows = wrapper.findAll('tbody tr')
    expect(rows[0].findAll('button').map(button => button.text())).toEqual(['tickets.complete', 'tickets.cancel'])
    expect(rows[1].findAll('button')).toHaveLength(0)
    await rows[0].findAll('button')[1].trigger('click')
    expect(close).not.toHaveBeenCalled()
    const dialog = wrapper.findComponent(ConfirmDialog)
    expect(dialog.props('show')).toBe(true)
    expect(dialog.props('danger')).toBe(true)
    dialog.vm.$emit('confirm')
    dialog.vm.$emit('confirm')
    await flushPromises()
    expect(close).toHaveBeenCalledTimes(1)
    expect(close).toHaveBeenCalledWith(42, 'cancel')
    expect(ticketAPI).toHaveBeenLastCalledWith(admin)
    expect(dialog.props('loading')).toBe(true)
    resolve({ ...fixture, status: 'cancelled' })
    await flushPromises()
    expect(dialog.props('show')).toBe(false)
    expect(list).toHaveBeenCalledTimes(2)
  })

  it('完成工单提交 complete 操作，失败时显示错误并允许重试', async () => {
    close.mockRejectedValue(new Error('保存失败'))
    const wrapper = render(true)
    await flushPromises()
    await wrapper.findAll('tbody button')[0].trigger('click')
    const dialog = wrapper.findComponent(ConfirmDialog)
    expect(dialog.props('danger')).toBe(false)
    dialog.vm.$emit('confirm')
    await flushPromises()
    expect(close).toHaveBeenCalledWith(42, 'complete')
    expect(wrapper.get('[role="alert"]').text()).toBeTruthy()
    expect(dialog.props('loading')).toBe(false)
    expect(dialog.props('show')).toBe(true)
  })

  it.each([false, true])('功能关闭时用户和后台只显示关闭提示，不加载工单列表（admin=%s）', async admin => {
    config.mockResolvedValue({ enabled: false })
    const wrapper = render(admin)
    await flushPromises()
    expect(list).not.toHaveBeenCalled()
    expect(wrapper.get('[role="status"]').text()).toBe('tickets.disabled')
    expect(syncTicketModuleEnabled).toHaveBeenLastCalledWith(false)
    expect(wrapper.find('table').exists()).toBe(false)
    expect(wrapper.findAll('button').map(button => button.text())).toEqual(['common.refresh'])
  })

  it('打开的关单确认收到关闭响应后收起对话框和原工单', async () => {
    close.mockRejectedValue({ status: 403, reason: 'TICKET_DISABLED' })
    const wrapper = render(true)
    await flushPromises()
    await wrapper.findAll('tbody button')[0].trigger('click')
    wrapper.findComponent(ConfirmDialog).vm.$emit('confirm')
    await flushPromises()
    expect(wrapper.get('[role="status"]').text()).toBe('tickets.disabled')
    expect(syncTicketModuleEnabled).toHaveBeenLastCalledWith(false)
    expect(wrapper.find('table').exists()).toBe(false)
    expect(wrapper.findComponent(ConfirmDialog).props('show')).toBe(false)
  })


  it('历史长标题保持单行省略，悬停仍能查看完整标题', async () => {
    const title = '历史工单标题'.repeat(30)
    list.mockResolvedValue({ items: [{ ...fixture, title }], total: 1 })
    const wrapper = render()
    await flushPromises()
    const link = wrapper.get('tbody a')
    expect(link.classes()).toContain('truncate')
    expect(link.attributes('title')).toBe(title)
    expect(link.text()).toBe(title)
  })


  it('列表同时显示待客服及待用户回复，结单角色独立于工单归属和当前页面身份', async () => {
    list.mockResolvedValue({ items: [
      { ...fixture, status: 'pending' },
      { ...fixture, id: 43, status: 'waiting_user' },
      { ...fixture, id: 44, status: 'completed', closed_by: 7, closed_by_role: 'admin' },
      { ...fixture, id: 45, status: 'cancelled', closed_by: 99, closed_by_role: 'user' },
      { ...fixture, id: 46, status: 'completed', closed_by: 7 },
    ], total: 5 })
    const wrapper = render(false, true)
    await flushPromises()
    const rows = wrapper.findAll('tbody tr')
    expect(rows[0].text()).toContain('tickets.statuses.pending')
    expect(rows[1].text()).toContain('tickets.statuses.waiting_user')
    expect(rows[0].find('[data-testid="ticket-close-role"]').exists()).toBe(false)
    expect(rows[2].get('[data-testid="ticket-close-role"]').text()).toBe('tickets.closedBy:tickets.closeRoles.admin')
    expect(rows[3].get('[data-testid="ticket-close-role"]').text()).toBe('tickets.closedBy:tickets.closeRoles.user')
    expect(rows[4].get('[data-testid="ticket-close-role"]').text()).toBe('tickets.closedBy:tickets.closeRoles.unknown')
  })

})
