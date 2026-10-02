import { mount, flushPromises } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ApiDocsView from '../ApiDocsView.vue'
import Select from '@/components/common/Select.vue'

const { copyToClipboard, fetchPublicSettings, state } = vi.hoisted(() => ({
  copyToClipboard: vi.fn(), fetchPublicSettings: vi.fn(),
  state: { authenticated: false },
}))
vi.mock('@/stores', () => ({
  useAppStore: () => ({ siteName: 'TokenRouter', siteLogo: '', apiBaseUrl: 'https://gateway.example.com/v1', publicSettingsLoaded: true, cachedPublicSettings: {}, fetchPublicSettings }),
  useAuthStore: () => ({ get isAuthenticated() { return state.authenticated }, isAdmin: false }),
}))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copied: false, copyToClipboard }) }))
vi.mock('@/composables/useTheme', () => ({ initTheme: vi.fn(), useTheme: () => ({ isDark: false, toggleTheme: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key }),
}))

async function render(path = '/docs') {
  const router = createRouter({ history: createMemoryHistory(), routes: [
    { path: '/docs/:documentId?', component: ApiDocsView },
    { path: '/:pathMatch(.*)*', component: { template: '<div />' } },
  ] })
  await router.push(path)
  await router.isReady()
  const wrapper = mount(ApiDocsView, { global: {
    plugins: [router],
    stubs: { AppLayout: { template: '<div data-testid="app-layout"><slot /></div>' }, Icon: true, LocaleSwitcher: true },
  } })
  await flushPromises()
  return { wrapper, router }
}

describe('API 文档页面', () => {
  beforeEach(() => { vi.clearAllMocks(); state.authenticated = false })

  it('访客可读默认指南，已登录用户复用应用布局', async () => {
    const { wrapper } = await render()
    expect(wrapper.find('article').text()).toContain('从一个端点开始接入')
    expect(wrapper.find('article').text()).toContain("export BASE_URL='https://gateway.example.com'")
    expect(fetchPublicSettings).not.toHaveBeenCalled()
    wrapper.unmount()
    state.authenticated = true
    const signedIn = await render('/docs/responses')
    expect(signedIn.wrapper.find('[data-testid="app-layout"]').exists()).toBe(true)
    expect(signedIn.wrapper.find('article').text()).toContain('/v1/responses')
    signedIn.wrapper.unmount()
  })

  it('搜索和自研平台选择框共同过滤目录，空结果可清除', async () => {
    const { wrapper } = await render('/docs/overview')
    const videoCatalog = wrapper.get('nav[aria-label="视频与异步任务"]')
    expect(videoCatalog.findAll('h3').map(heading => heading.text())).toEqual([
      'OpenAI Videos6', 'Seedance / 火山方舟3', 'Kling / 可灵6', 'Wan / 万相2', 'MiniMax2', 'Grok / xAI5',
    ])
    expect(videoCatalog.findAll('a')).toHaveLength(24)
    await wrapper.get('input[placeholder="apiDocs.search"]').setValue('Kling')
    expect(videoCatalog.findAll('h3').map(heading => heading.text())).toEqual(['Kling / 可灵6'])
    expect(wrapper.find('article').text()).toContain('/tasks')
    wrapper.findComponent(Select).vm.$emit('update:modelValue', 'deepseek')
    await flushPromises()
    expect(wrapper.find('article').text()).toContain('apiDocs.noResults')
    await wrapper.findAll('button').find(button => button.text() === 'apiDocs.reset')!.trigger('click')
    expect(wrapper.find('article').text()).toContain('/v1/messages')
    wrapper.unmount()
  })

  it('链接切换同步正文，可复制同一份 Markdown 与直达地址', async () => {
    const { wrapper, router } = await render('/docs/video-create')
    expect(wrapper.find('article').text()).toContain('/v1/videos')
    expect(wrapper.find('article').text()).toContain('OpenAI Videos')
    await wrapper.findAll('button').find(button => button.text().includes('apiDocs.copyMarkdown'))!.trigger('click')
    expect(copyToClipboard).toHaveBeenLastCalledWith(expect.stringContaining('## 请求参数'))
    await wrapper.get('button[aria-label="apiDocs.copyLink"]').trigger('click')
    expect(copyToClipboard).toHaveBeenLastCalledWith(expect.stringContaining('/docs/video-create'))
    await router.push('/docs/gemini-generate-content')
    await flushPromises()
    expect(wrapper.find('article').text()).toContain('x-goog-api-key')
    await router.push('/docs/unknown-document')
    await flushPromises()
    expect(wrapper.find('article').text()).toContain('apiDocs.notFound')
    wrapper.unmount()
  })

  it('移动目录能展开，选择接口后自动收起', async () => {
    const { wrapper } = await render()
    const trigger = wrapper.get('button[aria-controls="api-doc-catalog"]')
    expect(trigger.attributes('aria-expanded')).toBe('false')
    await trigger.trigger('click')
    expect(trigger.attributes('aria-expanded')).toBe('true')
    await wrapper.get('a[href="/docs/responses"]').trigger('click')
    await flushPromises()
    expect(trigger.attributes('aria-expanded')).toBe('false')
    wrapper.unmount()
  })
})
