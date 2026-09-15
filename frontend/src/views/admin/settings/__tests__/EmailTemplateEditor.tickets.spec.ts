import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const mocks = vi.hoisted(() => ({
  locale: { value: 'zh' }, getEmailTemplates: vi.fn(), getEmailTemplate: vi.fn(),
  previewEmailTemplate: vi.fn(), updateEmailTemplate: vi.fn(), showError: vi.fn(), showSuccess: vi.fn(),
}))
vi.mock('@/api', () => ({ adminAPI: { settings: mocks } }))
vi.mock('@/stores', () => ({ useAppStore: () => ({ showError: mocks.showError, showSuccess: mocks.showSuccess }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ locale: mocks.locale, t: (key: string) => key }) }))
import EmailTemplateEditor from '../EmailTemplateEditor.vue'
import Select from '@/components/common/Select.vue'

const events = ['ticket.staff_reply', 'ticket.completed', 'ticket.cancelled']
const template = { subject: '工单通知', html: '<p>{{ticket_title}}</p>', placeholders: ['ticket_id', 'ticket_title', 'ticket_url', 'unsubscribe_url'] }

describe('工单邮件模板', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.locale.value = 'zh'
    mocks.getEmailTemplates.mockResolvedValue({ events: events.map(value => ({ value, category: 'ticket', optional: true })), locales: ['zh', 'en'] })
    mocks.getEmailTemplate.mockResolvedValue(template)
    mocks.previewEmailTemplate.mockResolvedValue({ subject: '预览', html: '<p>工单预览</p>' })
    mocks.updateEmailTemplate.mockResolvedValue(template)
  })

  it.each([
    ['zh', ['工单回复通知', '工单完成通知', '工单撤销通知'], '客服每次提交新回复', '三类工单通知共用工单邮件退订偏好'],
    ['en', ['Ticket Reply', 'Ticket Completed', 'Ticket Withdrawn'], 'after every new support reply', 'share the same unsubscribe preference'],
  ] as const)('回复、完成与撤销都使用对应事件和本地化说明（%s）', async (locale, labels, replyHint, preferenceHint) => {
    mocks.locale.value = locale
    const wrapper = mount(EmailTemplateEditor, { global: { stubs: { Select: true } } })
    await flushPromises()
    const eventSelect = wrapper.findAllComponents(Select).find(item => item.attributes('id') === 'email-template-event')!
    expect(eventSelect.props('options')).toEqual(events.map((value, index) => ({ value, label: labels[index] })))
    expect(wrapper.text()).toContain(replyHint)
    expect(wrapper.text()).toContain(preferenceHint)
    expect(wrapper.text()).not.toContain('处理人员')
    for (const event of ['ticket.completed', 'ticket.cancelled']) {
      eventSelect.vm.$emit('update:modelValue', event)
      await flushPromises()
      expect(mocks.getEmailTemplate).toHaveBeenLastCalledWith(event, locale)
      expect(mocks.previewEmailTemplate).toHaveBeenLastCalledWith({ event, locale, subject: template.subject, html: template.html })
      expect(wrapper.text()).toContain(locale === 'zh' ? '自动过期不发送' : 'automatic expiry do not trigger this email')
      await wrapper.findAll('button').find(button => button.text() === 'admin.settings.emailTemplates.save')!.trigger('click')
      await flushPromises()
      expect(mocks.updateEmailTemplate).toHaveBeenLastCalledWith(event, locale, { subject: template.subject, html: template.html })
    }
    expect(mocks.showError).not.toHaveBeenCalled()
  })

  it('服务端未返回占位符列表时仍提供工单链接及标识', async () => {
    mocks.getEmailTemplate.mockResolvedValue({ subject: template.subject, html: template.html })
    const wrapper = mount(EmailTemplateEditor, { global: { stubs: { Select: true } } })
    await flushPromises()
    const placeholders = wrapper.findAll('button').map(button => button.text())
    expect(placeholders).toContain('{{ticket_id}}')
    expect(placeholders).toContain('{{ticket_title}}')
    expect(placeholders).toContain('{{ticket_url}}')
    expect(placeholders).toContain('{{unsubscribe_url}}')
  })
})
