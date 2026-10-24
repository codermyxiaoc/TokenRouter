import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useAdminSettingsStore } from '../adminSettings'

const { getSettings, getPaymentConfig } = vi.hoisted(() => ({
  getSettings: vi.fn(),
  getPaymentConfig: vi.fn(),
}))

vi.mock('@/api', () => ({
  adminAPI: {
    settings: { getSettings },
    payment: { getConfig: getPaymentConfig },
  },
}))

describe('掺水页面私有开关', () => {
  beforeEach(() => {
    localStorage.clear()
    setActivePinia(createPinia())
    getSettings.mockReset().mockResolvedValue({})
    getPaymentConfig.mockReset().mockResolvedValue({ data: { enabled: false } })
  })

  it('首次默认为关闭，不信任浏览器中残留的开启缓存', () => {
    localStorage.setItem('chanshui_enabled_cached', 'true')
    expect(useAdminSettingsStore().chanshuiEnabled).toBe(false)
  })

  it.each([undefined, false, 'true', 1, null])('只有明确布尔值允许访问，拒绝 %s', async enabled => {
    getSettings.mockResolvedValue({ chanshui_enabled: enabled })
    const store = useAdminSettingsStore()
    store.chanshuiEnabled = true
    await store.refreshChanshuiEnabled()
    expect(store.chanshuiEnabled).toBe(false)
    expect(getPaymentConfig).not.toHaveBeenCalled()
  })

  it('独立读取管理员设置，不受付款设置接口阻塞', async () => {
    getPaymentConfig.mockImplementation(() => new Promise(() => {}))
    getSettings.mockResolvedValue({ chanshui_enabled: true })
    const store = useAdminSettingsStore()
    await store.refreshChanshuiEnabled()
    expect(store.chanshuiEnabled).toBe(true)
    expect(getPaymentConfig).not.toHaveBeenCalled()
    expect(localStorage.getItem('chanshui_enabled_cached')).toBeNull()
  })

  it('请求失败关闭旧状态，下一次刷新允许恢复', async () => {
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    const store = useAdminSettingsStore()
    store.chanshuiEnabled = true
    getSettings.mockRejectedValueOnce(new Error('temporary unavailable'))
    await store.refreshChanshuiEnabled()
    expect(store.chanshuiEnabled).toBe(false)
    getSettings.mockResolvedValueOnce({ chanshui_enabled: true })
    await store.refreshChanshuiEnabled()
    expect(store.chanshuiEnabled).toBe(true)
    expect(getSettings).toHaveBeenCalledTimes(2)
    errorSpy.mockRestore()
  })

  it('普通后台设置刷新同步掺水开关', async () => {
    const store = useAdminSettingsStore()
    getSettings.mockResolvedValueOnce({ chanshui_enabled: true })
    await store.fetch()
    expect(store.chanshuiEnabled).toBe(true)
    getSettings.mockResolvedValueOnce({ chanshui_enabled: false })
    await store.fetch(true)
    expect(store.chanshuiEnabled).toBe(false)
  })
})
