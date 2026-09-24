import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useAdminSettingsStore } from '../adminSettings'

const { getSettings, getPaymentConfig } = vi.hoisted(() => ({
  getSettings: vi.fn(),
  getPaymentConfig: vi.fn()
}))

vi.mock('@/api', () => ({
  adminAPI: {
    settings: { getSettings },
    payment: { getConfig: getPaymentConfig }
  }
}))

describe('插件管理菜单开关', () => {
  beforeEach(() => {
    localStorage.clear()
    setActivePinia(createPinia())
    getSettings.mockReset().mockResolvedValue({})
    getPaymentConfig.mockReset().mockResolvedValue({ data: { enabled: false } })
  })

  it('未设置时不向已有管理员菜单添加插件入口', async () => {
    const store = useAdminSettingsStore()
    expect(store.pluginManagementEnabled).toBe(false)
    await store.fetch()
    expect(store.pluginManagementEnabled).toBe(false)
  })

  it('首次加载失败保留缓存，并允许普通 fetch 重试', async () => {
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    localStorage.setItem('plugin_management_enabled_cached', 'true')
    getSettings.mockRejectedValueOnce(new Error('temporary unavailable'))
    const store = useAdminSettingsStore()
    await store.fetch()
    expect(store.loaded).toBe(false)
    expect(store.pluginManagementEnabled).toBe(true)
    await store.fetch()
    expect(getSettings).toHaveBeenCalledTimes(2)
    expect(store.loaded).toBe(true)
    errorSpy.mockRestore()
  })

  it('读取并刷新持久化开关，不调用任何插件启停接口', async () => {
    getSettings.mockResolvedValueOnce({ plugin_management_enabled: true })
    const store = useAdminSettingsStore()
    await store.fetch()
    expect(store.pluginManagementEnabled).toBe(true)
    expect(localStorage.getItem('plugin_management_enabled_cached')).toBe('true')
    getSettings.mockResolvedValueOnce({ plugin_management_enabled: false })
    await store.fetch(true)
    expect(store.pluginManagementEnabled).toBe(false)
    expect(localStorage.getItem('plugin_management_enabled_cached')).toBe('false')
  })
})
