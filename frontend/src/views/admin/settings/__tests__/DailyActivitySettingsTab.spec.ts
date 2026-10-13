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
    expect(saveConfig).toHaveBeenCalledWith({ ...expected, enabled: true, recharge: { threshold: 50, reward_max: 1 }, consumption: { threshold: 25, reward_max: 0.8 } })
    expect(syncDailyActivityEnabled).toHaveBeenCalledWith(true)
    expect(showSuccess).toHaveBeenCalled(); wrapper.unmount()
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
