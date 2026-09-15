import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h, reactive } from 'vue'
import type { Ticket } from '@/api/tickets'

const { get, config, reply, close, download, update, saveAs } = vi.hoisted(() => ({ get: vi.fn(), config: vi.fn(), reply: vi.fn(), close: vi.fn(), download: vi.fn(), update: vi.fn(), saveAs: vi.fn() }))
const syncTicketModuleEnabled = vi.hoisted(() => vi.fn().mockResolvedValue(undefined))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ syncTicketModuleEnabled }) }))
vi.mock('@/api/client', () => ({ apiClient: {} }))
vi.mock('@/api/tickets', async (original) => ({ ...await original<typeof import('@/api/tickets')>(), ticketAPI: () => ({ get, config, reply, close, download, update }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, params?: { role?: string }) => key === 'tickets.closedBy' ? `${key}:${params?.role}` : key }) }))
vi.mock('vue-router', () => ({ useRoute: () => route, RouterLink: { props: ['to'], template: '<a :href="to"><slot /></a>' } }))
vi.mock('file-saver', () => ({ saveAs }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
import TicketDetailView from '../TicketDetailView.vue'
import TicketAttachments from '@/components/tickets/TicketAttachments.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import TicketAttachmentPreview from '@/components/tickets/TicketAttachmentPreview.vue'

enableAutoUnmount(afterEach)
const route = reactive({ params: { id: '42' } })

const attachment = { id: 9, message_id: 1, filename: 'receipt.pdf', content_type: 'application/pdf', size: 3, created_at: '2026-09-13T00:00:00Z' }
const fixture: Ticket = {
  id: 42, user_id: 7, user_email: 'user@example.test', user_name: '用户', title: '付款问题', content: '付款了', type: 'financial', priority: 'high', status: 'pending', created_at: '2026-09-13T00:00:00Z', updated_at: '2026-09-13T00:00:00Z',
  messages: [{ id: 1, ticket_id: 42, user_id: 7, sender_name: '用户', is_staff: false, content: '<img src=x onerror=alert(1)>', created_at: '2026-09-13T00:00:00Z', attachments: [attachment] }],
}
const AppLayout = defineComponent({ setup(_, { slots }) { return () => h('main', slots.default?.()) } })
// 保留弹窗显示、关闭和插槽卸载行为，附件渲染由独立组件测试覆盖。
const PreviewDialog = defineComponent({
  props: ['show', 'title', 'width'], emits: ['close'],
  template: '<section v-if="show" role="dialog"><button type="button" data-testid="close-preview" @click="$emit(\'close\')">关闭</button><slot /></section>',
})
function render(admin = false, realBadge = false) { return mount(TicketDetailView, { props: { admin }, global: { stubs: { AppLayout, TicketBadge: !realBadge, TicketAttachments: true, TicketAttachmentPreview: true, BaseDialog: PreviewDialog, ConfirmDialog: true, Icon: true, Select: true } } }) }

describe('工单详情', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    route.params.id = '42'
    get.mockResolvedValue(structuredClone(fixture))
    config.mockResolvedValue({ enabled: true, max_open_tickets: 3, max_attachments: 5, max_attachment_size_mb: 10, notify_on_staff_reply: false, auto_expire_hours: 72 })
    reply.mockResolvedValue(structuredClone(fixture))
    update.mockResolvedValue({ ...fixture, priority: 'normal' })
  })

  it('开启时允许回复，对话中的HTML保持纯文本', async () => {
    const wrapper = render()
    await flushPromises()
    expect(wrapper.find('form').exists()).toBe(true)
    expect(wrapper.text()).toContain('<img src=x onerror=alert(1)>')
    expect(wrapper.find('article img').exists()).toBe(false)
    await wrapper.get('#ticket-reply').setValue('请继续处理')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(reply).toHaveBeenCalledWith(42, '请继续处理', [], expect.any(String))
    expect((wrapper.get('#ticket-reply').element as HTMLTextAreaElement).value).toBe('')
  })

  it.each([false, true])('已结单的附件在当前工单弹窗预览，不打开新标签页或自动下载（admin=%s）', async admin => {
    get.mockResolvedValue({ ...fixture, status: 'completed' })
    const wrapper = render(admin)
    await flushPromises()
    expect(wrapper.find('form').exists()).toBe(false)
    expect(wrapper.findComponent(TicketAttachmentPreview).exists()).toBe(false)
    await wrapper.get('article button').trigger('click')
    const dialog = wrapper.findComponent(BaseDialog)
    expect(dialog.props('show')).toBe(true)
    expect(dialog.props('title')).toBe('receipt.pdf')
    expect(wrapper.findComponent(TicketAttachmentPreview).props()).toMatchObject({ ticketId: 42, attachmentId: 9, admin, showTitle: false })
    expect(wrapper.find('article a').exists()).toBe(false)
    expect(wrapper.find('[target="_blank"]').exists()).toBe(false)
    expect(download).not.toHaveBeenCalled()
    expect(saveAs).not.toHaveBeenCalled()
    expect(wrapper.html()).not.toContain('auth_token')
    await wrapper.get('[data-testid="close-preview"]').trigger('click')
    expect(dialog.props('show')).toBe(false)
    expect(wrapper.findComponent(TicketAttachmentPreview).exists()).toBe(false)
  })

  it.each([false, true])('打开及关闭附件预览保留回复草稿、已选文件和对话阅读位置（admin=%s）', async admin => {
    const wrapper = render(admin)
    await flushPromises()
    const draft = '保留这份尚未发送的回复'
    await wrapper.get('#ticket-reply').setValue(draft)
    const file = new File(['pdf'], 'draft.pdf')
    wrapper.findComponent(TicketAttachments).vm.$emit('update:modelValue', [file])
    const messages = wrapper.get('[data-testid="ticket-messages"]')
    const element = messages.element as HTMLElement
    Object.defineProperties(element, { scrollHeight: { value: 2000, configurable: true }, clientHeight: { value: 300, configurable: true } })
    element.scrollTop = 500
    await messages.trigger('scroll')
    await wrapper.get('article button').trigger('click')
    expect(wrapper.get('[role="dialog"]').exists()).toBe(true)
    expect((wrapper.get('#ticket-reply').element as HTMLTextAreaElement).value).toBe(draft)
    expect(element.scrollTop).toBe(500)
    await wrapper.get('[data-testid="close-preview"]').trigger('click')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(wrapper.findComponent(TicketAttachmentPreview).exists()).toBe(false)
    expect(wrapper.get('[data-testid="ticket-messages"]').element).toBe(element)
    expect(element.scrollTop).toBe(500)
    expect((wrapper.get('#ticket-reply').element as HTMLTextAreaElement).value).toBe(draft)
    expect(wrapper.findComponent(TicketAttachments).props('modelValue')).toEqual([file])
    expect(get).toHaveBeenCalledTimes(1)
    expect(reply).not.toHaveBeenCalled()
    expect(download).not.toHaveBeenCalled()
    expect(saveAs).not.toHaveBeenCalled()
  })

  it('切换工单关闭附件弹窗，不把上一个工单的附件留在新会话', async () => {
    const wrapper = render()
    await flushPromises()
    await wrapper.get('article button').trigger('click')
    expect(wrapper.findComponent(TicketAttachmentPreview).props('ticketId')).toBe(42)
    get.mockResolvedValue({ ...fixture, id: 43, title: '另一张工单', messages: [] })
    route.params.id = '43'
    await flushPromises()
    expect(get).toHaveBeenLastCalledWith(43)
    expect(wrapper.findComponent(BaseDialog).props('show')).toBe(false)
    expect(wrapper.findComponent(TicketAttachmentPreview).exists()).toBe(false)
    expect(wrapper.get('h1').text()).toBe('另一张工单')
  })

  it('预览期间工单功能关闭会清空弹窗和旧会话', async () => {
    const wrapper = render()
    await flushPromises()
    await wrapper.get('article button').trigger('click')
    wrapper.findComponent(TicketAttachmentPreview).vm.$emit('disabled')
    await flushPromises()
    expect(wrapper.findComponent(BaseDialog).props('show')).toBe(false)
    expect(wrapper.findComponent(TicketAttachmentPreview).exists()).toBe(false)
    expect(wrapper.find('[data-testid="ticket-messages"]').exists()).toBe(false)
    expect(wrapper.get('[role="status"]').text()).toBe('tickets.disabled')
  })
  it('允许只回复附件，空内容且无附件时不发送请求', async () => {
    const wrapper = render()
    await flushPromises()
    await wrapper.get('form').trigger('submit')
    expect(reply).not.toHaveBeenCalled()
    const file = new File(['pdf'], 'receipt.pdf')
    wrapper.findComponent(TicketAttachments).vm.$emit('update:modelValue', [file])
    await flushPromises()
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(reply).toHaveBeenCalledWith(42, '', [file], expect.any(String))
  })

  it('长对话使用独立滚动区域，刷新保留历史阅读位置，发送回复后返回最新消息', async () => {
    get.mockResolvedValue({ ...fixture, messages: Array.from({ length: 80 }, (_, index) => ({ ...fixture.messages![0], id: index + 1 })) })
    const wrapper = render()
    await flushPromises()
    const messages = wrapper.get('[data-testid="ticket-messages"]')
    const composer = wrapper.get('[data-testid="ticket-composer"]')
    expect(messages.findAll('article')).toHaveLength(80)
    expect(messages.element.contains(composer.element)).toBe(false)
    expect(wrapper.findAll('h1')).toHaveLength(1)
    const element = messages.element as HTMLElement
    Object.defineProperties(element, { scrollHeight: { value: 2000, configurable: true }, clientHeight: { value: 300, configurable: true } })
    element.scrollTop = 500
    await messages.trigger('scroll')
    expect(wrapper.text()).toContain('tickets.latest')
    await wrapper.findAll('button').find(button => button.text() === 'common.refresh')!.trigger('click')
    await flushPromises()
    expect(element.scrollTop).toBe(500)
    await wrapper.get('#ticket-reply').setValue('新的回复')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(element.scrollTop).toBe(2000)
    expect(wrapper.text()).not.toContain('tickets.latest')
  })

  it('后台详情也支持撤销，确认前不会提交请求', async () => {
    close.mockResolvedValue({ ...fixture, status: 'cancelled' })
    const wrapper = render(true)
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === 'tickets.cancel')!.trigger('click')
    expect(close).not.toHaveBeenCalled()
    const dialog = wrapper.findComponent(ConfirmDialog)
    expect(dialog.props('show')).toBe(true)
    dialog.vm.$emit('confirm')
    await flushPromises()
    expect(close).toHaveBeenCalledWith(42, 'cancel')
    expect(wrapper.find('form').exists()).toBe(false)
  })

  it('删除处理人员和接单操作，管理员只能选择低中高三级优先级', async () => {
    const wrapper = render(true)
    await flushPromises()
    expect(wrapper.text()).not.toContain('tickets.assignMe')
    expect(wrapper.text()).not.toContain('tickets.handler')
    expect(wrapper.text()).not.toContain('tickets.unassigned')
    const priority = wrapper.findComponent(Select)
    expect(priority.props('options').map((option: { value: string }) => option.value)).toEqual(['low', 'normal', 'high'])
    priority.vm.$emit('update:modelValue', 'normal')
    await flushPromises()
    expect(update).toHaveBeenCalledWith(42, { priority: 'normal' })
  })

  it.each([false, true])('总开关关闭时不加载详情或保留回复操作，重新开启刷新可恢复（admin=%s）', async admin => {
    const configValue = await config()
    config.mockResolvedValue({ ...configValue, enabled: false })
    const wrapper = render(admin)
    await flushPromises()
    expect(get).not.toHaveBeenCalled()
    expect(wrapper.get('[role="status"]').text()).toBe('tickets.disabled')
    expect(syncTicketModuleEnabled).toHaveBeenLastCalledWith(false)
    expect(wrapper.find('form').exists()).toBe(false)
    config.mockResolvedValue({ ...configValue, enabled: true })
    await wrapper.findAll('button').find(button => button.text() === 'common.refresh')!.trigger('click')
    await flushPromises()
    expect(wrapper.find('form').exists()).toBe(true)
  })

  it('已打开会话收到关闭响应后清空旧详情和确认框，不再显示可写入的回复区', async () => {
    reply.mockRejectedValue({ status: 403, reason: 'TICKET_DISABLED' })
    const wrapper = render()
    await flushPromises()
    await wrapper.get('#ticket-reply').setValue('提交时关闭了功能')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.get('[role="status"]').text()).toBe('tickets.disabled')
    expect(syncTicketModuleEnabled).toHaveBeenLastCalledWith(false)
    expect(wrapper.find('[data-testid="ticket-messages"]').exists()).toBe(false)
    expect(wrapper.find('form').exists()).toBe(false)
    expect(wrapper.findComponent(ConfirmDialog).props('show')).toBe(false)
  })

  it('详情只保留一个可省略的主标题，信息和操作默认展示且附件仍可折叠', async () => {
    const title = '历史工单标题'.repeat(30)
    get.mockResolvedValue({ ...fixture, title, order_id: 91, order: { id: 91, out_trade_no: 'ORDER91', amount: 10, pay_amount: 72, currency: 'CNY', status: 'completed' } })
    const wrapper = render(true)
    await flushPromises()
    expect(wrapper.findAll('h1')).toHaveLength(1)
    expect(wrapper.get('h1').classes()).toContain('truncate')
    expect(wrapper.get('h1').attributes('title')).toBe(title)
    expect(wrapper.get('h1').text()).toBe(title)
    const info = wrapper.get('.ticket-info')
    expect(info.find('details').exists()).toBe(false)
    expect(info.find('summary').exists()).toBe(false)
    expect(info.findAll('p').some(paragraph => paragraph.text() === title)).toBe(false)
    expect(info.text()).toContain('ORDER91')
    expect(info.text()).toContain('user@example.test')
    expect(info.findAll('button').map(button => button.text())).toEqual(['tickets.cancel', 'tickets.complete'])
    expect(wrapper.find('[data-testid="ticket-composer"] details').exists()).toBe(true)
  })


  it.each([
    [false, 'completed', 'admin', 'admin'],
    [true, 'cancelled', 'user', 'user'],
    [true, 'completed', undefined, 'unknown'],
  ] as const)('详情显示服务端结单角色而不暴露账户ID（admin=%s/status=%s/role=%s）', async (admin, status, role, expectedRole) => {
    get.mockResolvedValue({ ...fixture, status, closed_by: 98765432, closed_by_role: role })
    const wrapper = render(admin, true)
    await flushPromises()
    expect(wrapper.get('[data-testid="ticket-close-role"]').text()).toBe(`tickets.closedBy:tickets.closeRoles.${expectedRole}`)
    expect(wrapper.get('.ticket-info').text()).not.toContain('98765432')
    expect(wrapper.find('[data-testid="ticket-composer"]').exists()).toBe(false)
  })

  it('当前工单完成后立即显示响应中的结单角色', async () => {
    close.mockResolvedValue({ ...fixture, status: 'completed', closed_by: 7, closed_by_role: 'admin' })
    const wrapper = render(true, true)
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text() === 'tickets.complete')!.trigger('click')
    wrapper.findComponent(ConfirmDialog).vm.$emit('confirm')
    await flushPromises()
    expect(wrapper.get('[data-testid="ticket-close-role"]').text()).toBe('tickets.closedBy:tickets.closeRoles.admin')
    expect(wrapper.find('[data-testid="ticket-composer"]').exists()).toBe(false)
  })

})
