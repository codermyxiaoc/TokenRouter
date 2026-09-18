import { afterEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, mount } from '@vue/test-utils'
import CustomPageView from '../CustomPageView.vue'

const { menu } = vi.hoisted(() => ({
  menu: { id: 'docs', label: '文档', url: 'https://example.com/docs', hide_open_button: undefined as boolean | undefined },
}))

vi.mock('vue-router', () => ({ useRoute: () => ({ params: { id: 'docs' } }) }))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'), useI18n: () => ({ t: (key: string) => key, locale: { value: 'zh-CN' } }) }))
vi.mock('@/stores', () => ({
  useAppStore: () => ({ publicSettingsLoaded: true, cachedPublicSettings: { custom_menu_items: [menu] } }),
}))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ isAdmin: false, user: null, token: null }) }))
vi.mock('@/stores/adminSettings', () => ({ useAdminSettingsStore: () => ({ customMenuItems: [] }) }))
vi.mock('@/api/client', () => ({ buildApiUrl: (path: string) => path }))

enableAutoUnmount(afterEach)

describe('自定义页面打开按钮', () => {
  // 省略配置时保留旧行为；隐藏入口不能影响嵌入页面本身。
  it.each([undefined, false, true])('按设置 %s 控制按钮并保留 iframe', (hidden) => {
    menu.hide_open_button = hidden
    const wrapper = mount(CustomPageView, {
      global: { stubs: { AppLayout: { template: '<div><slot /></div>' }, Icon: true } },
    })
    expect(wrapper.find('.custom-open-fab').exists()).toBe(hidden !== true)
    expect(wrapper.get('iframe').attributes('src')).toContain('https://example.com/docs')
    if (!hidden) {
      expect(wrapper.get('.custom-open-fab').attributes('rel')).toBe('noopener noreferrer')
    }
  })
})
