import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import UpstreamBillingSettingsModal from '../UpstreamBillingSettingsModal.vue'

const { getSettings, updateSettings, showError } = vi.hoisted(() => ({ getSettings: vi.fn(), updateSettings: vi.fn(), showError: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { accounts: { getUpstreamBillingProbeSettings: getSettings, updateUpstreamBillingProbeSettings: updateSettings } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError, showSuccess: vi.fn() }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const mountModal = () => mount(UpstreamBillingSettingsModal, {
  props: { show: true },
  global: { stubs: { BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' } } }
})

describe('UpstreamBillingSettingsModal', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getSettings.mockResolvedValue({ enabled: true, interval_minutes: 30 })
    updateSettings.mockImplementation(async settings => settings)
  })

  it('loads and saves the global switch and interval independently', async () => {
    const wrapper = mountModal()
    await flushPromises()
    await wrapper.get('[data-testid="upstream-billing-global-enabled"]').trigger('click')
    await wrapper.get('input').setValue(1440)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(updateSettings).toHaveBeenCalledTimes(1)
    expect(updateSettings).toHaveBeenCalledWith({ enabled: false, interval_minutes: 1440 })
    expect(wrapper.emitted('saved')?.[0]).toEqual([{ enabled: false, interval_minutes: 1440 }])
    wrapper.unmount()
  })

  it.each(['', 0, 4, 1441, 5.5])('rejects an invalid interval %s', async value => {
    const wrapper = mountModal()
    await flushPromises()
    await wrapper.get('input').setValue(value)
    await wrapper.get('form').trigger('submit')
    expect(updateSettings).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledWith('admin.accounts.upstreamBilling.intervalInvalid')
    wrapper.unmount()
  })

  it('does not write default settings when loading fails', async () => {
    getSettings.mockRejectedValue(new Error('offline'))
    const wrapper = mountModal()
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe('admin.accounts.upstreamBilling.settingsFailed')
    expect(wrapper.get('button[type="submit"]').attributes('disabled')).toBeDefined()
    await wrapper.get('form').trigger('submit')
    expect(updateSettings).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('coalesces repeated saves and ignores completion after reopening', async () => {
    let resolveSave!: (value: { enabled: boolean; interval_minutes: number }) => void
    updateSettings.mockImplementation(() => new Promise(resolve => { resolveSave = resolve }))
    const wrapper = mountModal()
    await flushPromises()
    await wrapper.get('form').trigger('submit')
    await wrapper.get('form').trigger('submit')
    expect(updateSettings).toHaveBeenCalledTimes(1)
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    resolveSave({ enabled: true, interval_minutes: 30 })
    await flushPromises()
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(wrapper.emitted('saved')).toBeUndefined()
    wrapper.unmount()
  })
})
