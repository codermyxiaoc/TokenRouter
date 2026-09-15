import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import type { TicketSettings } from '@/api/tickets'

const { create, getMyOrders } = vi.hoisted(() => ({ create: vi.fn(), getMyOrders: vi.fn() }))
vi.mock('@/api/tickets', async (original) => ({ ...await original<typeof import('@/api/tickets')>(), ticketAPI: () => ({ create }) }))
vi.mock('@/api/payment', () => ({ paymentAPI: { getMyOrders } }))
vi.mock('@/api/client', () => ({ apiClient: {} }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
import CreateTicketDialog from '../CreateTicketDialog.vue'
import Select from '@/components/common/Select.vue'

const settings: TicketSettings = { enabled: true, max_open_tickets: 3, max_attachments: 5, max_attachment_size_mb: 10, notify_on_staff_reply: false, auto_expire_hours: 72 }
const BaseDialog = defineComponent({ setup(_, { slots }) { return () => h('div', [slots.default?.(), slots.footer?.()]) } })

function render() {
  return mount(CreateTicketDialog, { props: { show: true, settings }, global: { stubs: { BaseDialog, Select: true, TicketAttachments: true } } })
}

describe('新建工单', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    create.mockResolvedValue({ id: 42 })
    getMyOrders.mockResolvedValue({ data: { items: [{ id: 91, amount: 10, pay_amount: 72, currency: 'CNY', out_trade_no: 'ORDER91', created_at: '2026-09-13' }], total: 1 } })
  })

  it('默认咨询，创建和筛选使用相同的三种工单类型', async () => {
    const wrapper = render()
    const typeSelect = wrapper.findAllComponents(Select)[0]
    expect(typeSelect.props('options').map((option: { value: string }) => option.value)).toEqual(['consultation', 'financial', 'technical'])
    expect(typeSelect.props('modelValue')).toBe('consultation')
    const prioritySelect = wrapper.findAllComponents(Select)[1]
    expect(prioritySelect.props('options').map((option: { value: string }) => option.value)).toEqual(['low', 'normal', 'high'])
    expect(prioritySelect.props('modelValue')).toBe('normal')
    await wrapper.get('#ticket-title').setValue('使用咨询')
    await wrapper.get('#ticket-content').setValue('如何使用')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(create.mock.calls[0][0].type).toBe('consultation')
  })

  it('仅财务加载本人订单，切换类型后清除隐藏关联', async () => {
    const wrapper = render()
    await wrapper.get('#ticket-title').setValue('订单问题')
    await wrapper.get('#ticket-content').setValue('补充说明')
    expect(getMyOrders).not.toHaveBeenCalled()
    wrapper.findAllComponents(Select)[0].vm.$emit('update:modelValue', 'financial')
    await flushPromises()
    expect(getMyOrders).toHaveBeenCalledWith({ page: 1, page_size: 50 })
    const orderSelect = wrapper.findAllComponents(Select).find(component => component.attributes('id') === 'ticket-order')!
    expect(orderSelect.props('options')[0].label).toContain('CNY 72.00')
    orderSelect.vm.$emit('update:modelValue', 91)
    await flushPromises()
    wrapper.findAllComponents(Select)[0].vm.$emit('update:modelValue', 'technical')
    await flushPromises()
    await wrapper.get('form').trigger('submit')
    expect(create.mock.calls[0][0]).toMatchObject({ type: 'technical', title: '订单问题' })
    expect(create.mock.calls[0][0]).not.toHaveProperty('order_id')
  })

  it('财务工单可以不关联订单，相同草稿在网络失败后重试复用键', async () => {
    create.mockRejectedValueOnce(new Error('network')).mockResolvedValueOnce({ id: 42 })
    const wrapper = render()
    await wrapper.get('#ticket-title').setValue('财务咨询')
    await wrapper.get('#ticket-content').setValue('没有订单也能咨询')
    wrapper.findAllComponents(Select)[0].vm.$emit('update:modelValue', 'financial')
    await flushPromises()
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(create).toHaveBeenCalledTimes(2)
    expect(create.mock.calls[0][0]).not.toHaveProperty('order_id')
    expect(create.mock.calls[0][2]).toBe(create.mock.calls[1][2])
    expect(wrapper.emitted('created')).toEqual([[{ id: 42 }]])
  })

  it('提交未完成期间禁止重复发送，保留输入直到收到成功结果', async () => {
    let resolve!: (value: { id: number }) => void
    create.mockReturnValue(new Promise(done => { resolve = done }))
    const wrapper = render()
    await wrapper.get('#ticket-title').setValue('技术问题')
    await wrapper.get('#ticket-content').setValue('需要帮助')
    await wrapper.get('form').trigger('submit')
    await wrapper.get('form').trigger('submit')
    expect(create).toHaveBeenCalledTimes(1)
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()
    resolve({ id: 42 })
    await flushPromises()
    expect(wrapper.emitted('created')).toHaveLength(1)
  })

  it('创建时收到模块关闭响应后停止提交，并通知列表关闭创建入口', async () => {
    create.mockRejectedValue({ status: 403, reason: 'TICKET_DISABLED' })
    const wrapper = render()
    await wrapper.get('#ticket-title').setValue('咨询')
    await wrapper.get('#ticket-content').setValue('问题')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.emitted('disabled')).toEqual([[]])
    expect(wrapper.get('[role="status"]').text()).toBe('tickets.disabled')
    expect(wrapper.find('form').exists()).toBe(false)
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()
  })

  it.each(['a', '中', '😀'])('按码点接收50字并排除首尾空格（%s）', async character => {
    const wrapper = render()
    const title = character.repeat(50)
    await wrapper.get('#ticket-title').setValue(`  ${title}  `)
    await wrapper.get('#ticket-content').setValue('问题内容')
    expect(wrapper.get('#ticket-title').attributes('maxlength')).toBeUndefined()
    expect(wrapper.get('#ticket-title-count').text()).toBe('50 / 50')
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeUndefined()
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(create.mock.calls[0][0].title).toBe(title)
  })

  it.each(['a', '中', '😀'])('51字显示提示并阻止直接触发表单绕过禁用按钮（%s）', async character => {
    const wrapper = render()
    const title = character.repeat(51)
    await wrapper.get('#ticket-title').setValue(title)
    await wrapper.get('#ticket-content').setValue('问题内容')
    expect(wrapper.get('#ticket-title-count').text()).toBe('51 / 50')
    expect(wrapper.get('#ticket-title').attributes('aria-invalid')).toBe('true')
    expect((wrapper.get('#ticket-title').element as HTMLInputElement).value).toBe(title)
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[role="alert"]').text()).toBe('tickets.errors.TICKET_TITLE_TOO_LONG')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(create).not.toHaveBeenCalled()
  })

  it('中文输入法的中间拼音不计数且不能提交，合成结束后按最终汉字验证', async () => {
    const wrapper = render()
    const input = wrapper.get('#ticket-title')
    await input.setValue('中'.repeat(49))
    await wrapper.get('#ticket-content').setValue('问题内容')
    await input.trigger('compositionstart')
    ;(input.element as HTMLInputElement).value = '中'.repeat(49) + 'wen'
    await input.trigger('input')
    expect(wrapper.get('#ticket-title-count').text()).toBe('49 / 50')
    await wrapper.get('form').trigger('submit')
    expect(create).not.toHaveBeenCalled()
    ;(input.element as HTMLInputElement).value = '中'.repeat(49) + '文'
    await input.trigger('compositionend')
    await flushPromises()
    expect(wrapper.get('#ticket-title-count').text()).toBe('50 / 50')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(create.mock.calls[0][0].title).toBe('中'.repeat(49) + '文')
  })

})
