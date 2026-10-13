import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const { config, saveConfig, syncDailyActivityEnabled, showSuccess } = vi.hoisted(() => ({ config: vi.fn(), saveConfig: vi.fn(), syncDailyActivityEnabled: vi.fn(), showSuccess: vi.fn() }))
vi.mock('@/api/dailyActivities', async importOriginal => ({ ...(await importOriginal<typeof import('@/api/dailyActivities')>()), dailyActivitiesAPI: { config, saveConfig } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ syncDailyActivityEnabled, showSuccess }) }))
vi.mock('@/composables/useBalanceDisplay', () => ({ useBalanceDisplay: () => ({ balanceUnitName: 'USD' }) }))
vi.mock('vue-i18n', async importOriginal => ({ ...(await importOriginal<typeof import('vue-i18n')>()), useI18n: () => ({ t: (key: string) => key, locale: { value: 'zh' } }) }))
import DailyActivitySettingsTab from '../DailyActivitySettingsTab.vue'

const defaults = () => ({ enabled: false, started_at: '2026-10-13T00:00:00Z', daily_gift: { threshold: 10, reward_max: 0.5 }, recharge: { threshold: 50, reward_max: 0.8 }, consumption: { threshold: 50, reward_max: 0.8 } })
describe('每日活动独立设置', () => {
  beforeEach(() => { vi.clearAllMocks(); config.mockResolvedValue(defaults()); saveConfig.mockImplementation(async input => ({ ...input, started_at: defaults().started_at })) })
  it('使用独立按钮保存三个规则，不可覆盖首次启用时间，成功刷新菜单开关', async () => {
    const wrapper = mount(DailyActivitySettingsTab); await flushPromises()
    await wrapper.get('#daily-activity-enabled').trigger('click')
    await wrapper.get('#consumption-threshold').setValue('25')
    await wrapper.get('#recharge-reward-max').setValue('1')
    const button = wrapper.get('[data-testid="save-daily-activities"]')
    expect(button.attributes('type')).toBe('button')
    expect(wrapper.find('form').exists()).toBe(false)
    await button.trigger('click'); await flushPromises()
    const expected = { ...defaults() }
    Reflect.deleteProperty(expected, 'started_at')
    expect(saveConfig).toHaveBeenCalledWith({ ...expected, enabled: true, include_redeem_balance: false, include_admin_balance: false, daily_gift: { ...expected.daily_gift, enabled: true }, recharge: { enabled: true, threshold: 50, reward_max: 1 }, consumption: { enabled: true, threshold: 25, reward_max: 0.8 } })
    expect(syncDailyActivityEnabled).toHaveBeenCalledWith(true)
    expect(showSuccess).toHaveBeenCalled(); wrapper.unmount()
  })
  it('旧配置未返回来源字段时两个开关默认关闭，单独开启互不影响', async () => {
    const wrapper = mount(DailyActivitySettingsTab); await flushPromises()
    expect(wrapper.get('#daily-activity-include-redeem').attributes('aria-checked')).toBe('false')
    expect(wrapper.get('#daily-activity-include-admin').attributes('aria-checked')).toBe('false')
    await wrapper.get('#daily-activity-include-redeem').trigger('click')
    await wrapper.get('[data-testid="save-daily-activities"]').trigger('click'); await flushPromises()
    expect(saveConfig).toHaveBeenLastCalledWith(expect.objectContaining({ include_redeem_balance: true, include_admin_balance: false }))
    await wrapper.get('#daily-activity-include-redeem').trigger('click')
    await wrapper.get('#daily-activity-include-admin').trigger('click')
    await wrapper.get('[data-testid="save-daily-activities"]').trigger('click'); await flushPromises()
    expect(saveConfig).toHaveBeenLastCalledWith(expect.objectContaining({ include_redeem_balance: false, include_admin_balance: true }))
    wrapper.unmount()
  })
  it('旧配置的三个活动默认启用，独立关闭后保留各自规则与其他开关', async () => {
    const wrapper = mount(DailyActivitySettingsTab); await flushPromises()
    for (const type of ['daily_gift', 'recharge', 'consumption']) {
      expect(wrapper.get(`#${type}-enabled`).attributes('aria-checked')).toBe('true')
    }
    await wrapper.get('#recharge-enabled').trigger('click')
    await wrapper.get('#daily-activity-include-admin').trigger('click')
    await wrapper.get('[data-testid="save-daily-activities"]').trigger('click'); await flushPromises()
    expect(saveConfig).toHaveBeenLastCalledWith(expect.objectContaining({
      include_admin_balance: true,
      daily_gift: { ...defaults().daily_gift, enabled: true },
      recharge: { ...defaults().recharge, enabled: false },
      consumption: { ...defaults().consumption, enabled: true },
    }))
    expect(wrapper.get('#recharge-enabled').attributes('aria-checked')).toBe('false')
    wrapper.unmount()
  })
  it('读取单独关闭的活动时不自动重开，失败保存仍保留状态', async () => {
    config.mockResolvedValue({ ...defaults(), daily_gift: { ...defaults().daily_gift, enabled: false } })
    saveConfig.mockRejectedValueOnce(new Error('unavailable'))
    const wrapper = mount(DailyActivitySettingsTab); await flushPromises()
    expect(wrapper.get('#daily_gift-enabled').attributes('aria-checked')).toBe('false')
    await wrapper.get('#consumption-enabled').trigger('click')
    await wrapper.get('[data-testid="save-daily-activities"]').trigger('click'); await flushPromises()
    expect(wrapper.get('#daily_gift-enabled').attributes('aria-checked')).toBe('false')
    expect(wrapper.get('#consumption-enabled').attributes('aria-checked')).toBe('false')
    expect(wrapper.get('#recharge-enabled').attributes('aria-checked')).toBe('true')
    expect(syncDailyActivityEnabled).not.toHaveBeenCalled()
    wrapper.unmount()
  })
  it('读取已开启的来源且保存时禁止重复切换，成功后保留服务端结果', async () => {
    config.mockResolvedValue({ ...defaults(), include_redeem_balance: true, include_admin_balance: true })
    let resolve!: (value: unknown) => void
    saveConfig.mockReturnValue(new Promise(r => { resolve = r }))
    const wrapper = mount(DailyActivitySettingsTab); await flushPromises()
    expect(wrapper.get('#daily-activity-include-redeem').attributes('aria-checked')).toBe('true')
    expect(wrapper.get('#daily-activity-include-admin').attributes('aria-checked')).toBe('true')
    await wrapper.get('[data-testid="save-daily-activities"]').trigger('click')
    expect(wrapper.get('#daily-activity-include-redeem').attributes('disabled')).toBeDefined()
    expect(wrapper.get('#daily-activity-include-admin').attributes('disabled')).toBeDefined()
    for (const type of ['daily_gift', 'recharge', 'consumption']) {
      expect(wrapper.get(`#${type}-enabled`).attributes('disabled')).toBeDefined()
    }
    resolve({ ...defaults(), include_redeem_balance: true, include_admin_balance: true }); await flushPromises()
    expect(wrapper.get('#daily-activity-include-redeem').attributes('aria-checked')).toBe('true')
    expect(wrapper.get('#daily-activity-include-admin').attributes('aria-checked')).toBe('true')
    wrapper.unmount()
  })
  it.each(['0.09', '-1', '1.001', '', '1000000.01'])('无效奖励上限%s不可保存', async value => {
    const wrapper = mount(DailyActivitySettingsTab); await flushPromises()
    await wrapper.get('#daily_gift-reward-max').setValue(value)
    expect(wrapper.get('[data-testid="save-daily-activities"]').attributes('disabled')).toBeDefined()
    expect(saveConfig).not.toHaveBeenCalled(); wrapper.unmount()
  })
  it('加载失败不能拿默认配置覆盖，刷新后可恢复', async () => {
    config.mockRejectedValueOnce(new Error('unavailable'))
    const wrapper = mount(DailyActivitySettingsTab); await flushPromises()
    expect(wrapper.find('input').exists()).toBe(false)
    expect(wrapper.find('[data-testid="save-daily-activities"]').exists()).toBe(false)
    await wrapper.get('button').trigger('click'); await flushPromises()
    expect(wrapper.find('#daily_gift-threshold').exists()).toBe(true); wrapper.unmount()
  })
  it('保存失败保留编辑值且不更新菜单', async () => {
    saveConfig.mockRejectedValueOnce(new Error('conflict'))
    const wrapper = mount(DailyActivitySettingsTab); await flushPromises()
    await wrapper.get('#recharge-threshold').setValue('20')
    await wrapper.get('[data-testid="save-daily-activities"]').trigger('click'); await flushPromises()
    expect(wrapper.get<HTMLInputElement>('#recharge-threshold').element.value).toBe('20')
    expect(syncDailyActivityEnabled).not.toHaveBeenCalled(); wrapper.unmount()
  })
})
