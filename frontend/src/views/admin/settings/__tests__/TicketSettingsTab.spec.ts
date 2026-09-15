import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

const { config, save, showSuccess, syncTicketModuleEnabled } = vi.hoisted(() => ({ config: vi.fn(), save: vi.fn(), showSuccess: vi.fn(), syncTicketModuleEnabled: vi.fn() }))
vi.mock('@/api/tickets', () => ({ ticketAPI: () => ({ config }), saveTicketSettings: save }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showSuccess, syncTicketModuleEnabled }) }))
vi.mock('vue-i18n', async () => {
  const { default: zh } = await import('@/i18n/locales/zh/tickets')
  const labels: Record<string, string> = zh.tickets.settings
  return { useI18n: () => ({ t: (key: string) => key.startsWith('tickets.settings.') ? labels[key.slice('tickets.settings.'.length)] ?? key : key }) }
})
import TicketSettingsTab from '../TicketSettingsTab.vue'

const defaults = { enabled: true, max_open_tickets: 3, max_attachments: 5, max_attachment_size_mb: 10, notify_on_staff_reply: false, auto_expire_hours: 72 }
describe('工单设置', () => {
  beforeEach(() => { vi.clearAllMocks(); config.mockResolvedValue({ ...defaults }); save.mockImplementation(async value => value) })

  it('允许关闭附件及自动过期，使用普通按钮保存独立配置', async () => {
    const wrapper = mount(TicketSettingsTab, { global: { stubs: { Toggle: true } } })
    await flushPromises()
    await wrapper.get('#ticket-setting-max_attachments').setValue(0)
    await wrapper.get('#ticket-setting-auto_expire_hours').setValue(0)
    const button = wrapper.findAll('button').find(item => item.text() === 'common.save')!
    expect(button.attributes('type')).toBe('button')
    expect(wrapper.find('form').exists()).toBe(false)
    await button.trigger('click')
    await flushPromises()
    expect(save).toHaveBeenCalledWith({ ...defaults, max_attachments: 0, auto_expire_hours: 0 })
    expect(showSuccess).toHaveBeenCalled()
  })

  it('配置加载失败不允许拿默认值覆盖服务器', async () => {
    config.mockRejectedValue(new Error('unavailable'))
    const wrapper = mount(TicketSettingsTab, { global: { stubs: { Toggle: true } } })
    await flushPromises()
    expect(wrapper.find('input').exists()).toBe(false)
    expect(wrapper.findAll('button').some(item => item.text() === 'common.save')).toBe(false)
    expect(save).not.toHaveBeenCalled()
  })

  it('显式保存 false 关闭整个模块并同步公开设置缓存', async () => {
    const wrapper = mount(TicketSettingsTab)
    await flushPromises()
    const toggle = wrapper.get('#tickets-enabled')
    await toggle.trigger('click')
    const button = wrapper.findAll('button').find(item => item.text() === 'common.save')!
    await button.trigger('click')
    await flushPromises()
    expect(save).toHaveBeenCalledWith({ ...defaults, enabled: false })
    expect(syncTicketModuleEnabled).toHaveBeenCalledWith(false)
    expect(showSuccess).toHaveBeenCalled()
  })

  it('保存失败时不改变已生效的模块开关', async () => {
    save.mockRejectedValueOnce(new Error('unavailable'))
    const wrapper = mount(TicketSettingsTab)
    await flushPromises()
    await wrapper.get('#tickets-enabled').trigger('click')
    await wrapper.findAll('button').find(item => item.text() === 'common.save')!.trigger('click')
    await flushPromises()
    expect(syncTicketModuleEnabled).not.toHaveBeenCalled()
    expect(showSuccess).not.toHaveBeenCalled()
  })

  it('客服操作通知沿用原键，说明涵盖每次回复和客服结单', async () => {
    const wrapper = mount(TicketSettingsTab)
    await flushPromises()
    expect(wrapper.get('label[for="tickets-notify"]').text()).toBe('客服操作邮件通知')
    expect(wrapper.text()).toContain('客服每次回复、完成或撤销工单时')
    expect(wrapper.text()).toContain('用户自行结单和自动过期不发送')
    await wrapper.get('#tickets-notify').trigger('click')
    await wrapper.findAll('button').find(item => item.text() === 'common.save')!.trigger('click')
    await flushPromises()
    expect(save).toHaveBeenCalledWith({ ...defaults, notify_on_staff_reply: true })
  })
})
