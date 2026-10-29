import { mount, flushPromises } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ApiDocsView from '../ApiDocsView.vue'
import Select from '@/components/common/Select.vue'

const { copyToClipboard, fetchPublicSettings, getCatalog, getDocument, state } = vi.hoisted(() => ({
  copyToClipboard: vi.fn(), fetchPublicSettings: vi.fn(), getCatalog: vi.fn(), getDocument: vi.fn(),
  state: { authenticated: false },
}))
vi.mock('@/api/apiDocs', () => ({ apiDocsAPI: { getCatalog, getDocument } }))

const emptyCatalog = () => ({ categories: [], documents: [] })
// 构造一条文档叠加记录，未指定的字段沿用内置值。
const docOverlay = (id: string, extra: Record<string, unknown> = {}) => ({
  id, category_id: null, title: null, has_markdown: false, sort: null, hidden: false, custom: false, version: 1, updated_at: '', ...extra,
})
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
  beforeEach(() => { vi.clearAllMocks(); state.authenticated = false; getCatalog.mockResolvedValue(emptyCatalog()) })

  // 管理员改写的 Markdown 优先渲染，{{BASE_URL}} 替换为网关地址，站内链接走前端路由；读取失败回退内置文档。
  it('渲染管理员改写的 Markdown 并保留内置回退', async () => {
    getCatalog.mockResolvedValue({ categories: [], documents: [docOverlay('responses', { has_markdown: true })] })
    getDocument.mockResolvedValue(docOverlay('responses', { markdown: '# 自定义 Responses\n\n调用 `{{BASE_URL}}/v1/responses`\n\n## 第一节\n\n[看总览](/docs/overview)\n\n<script>alert(1)</script>' }))
    const { wrapper, router } = await render('/docs/responses')
    const article = wrapper.get('article')
    expect(article.find('h1').text()).toBe('自定义 Responses')
    expect(article.text()).toContain('https://gateway.example.com/v1/responses')
    expect(article.html()).not.toContain('<script>')
    await article.get('a[href="/docs/overview"]').trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.path).toBe('/docs/overview')
    await wrapper.findAll('button').find(button => button.text().includes('apiDocs.copyMarkdown'))!.trigger('click')
    expect(copyToClipboard).toHaveBeenLastCalledWith(expect.stringContaining('# 接口总览'))
    wrapper.unmount()
    getCatalog.mockRejectedValue(new Error('offline'))
    const fallback = await render('/docs/responses')
    expect(fallback.wrapper.get('article').text()).toContain('/v1/responses')
    fallback.wrapper.unmount()
  })

  it('沿用现有目录与正文渲染缓存指南，并复制相同的归属和费用说明', async () => {
    const { wrapper, router } = await render('/docs/gemini-cache-create')
    expect(wrapper.get('article').text()).toContain('Gemini 创建显式缓存')
    expect(wrapper.get('article').text()).toContain('/v1beta/cachedContents')
    expect(wrapper.get('article').text()).toContain('不支持复合 Key、智能路由 Key')
    expect(wrapper.get('article').text()).toContain('Token × 小时')
    await wrapper.findAll('button').find(button => button.text().includes('apiDocs.copyMarkdown'))!.trigger('click')
    expect(copyToClipboard).toHaveBeenLastCalledWith(expect.stringContaining('API Key 和分组隔离'))
    await router.push('/docs/gemini-cache-generate')
    await flushPromises()
    expect(wrapper.get('article').text()).toContain('cachedContent')
    expect(wrapper.get('article').text()).toContain('固定使用持有该缓存的账号')
    wrapper.unmount()
  })

  it('Seedance 首尾帧指南可直接打开并复制包含 adaptive 与角色的完整说明', async () => {
    const { wrapper, router } = await render('/docs/kemei-seedance25-frames')
    expect(wrapper.get('article').text()).toContain('doubao-seedance-2-5-260628')
    expect(wrapper.get('article').text()).toContain('ratio 必须为 adaptive')
    expect(wrapper.get('article').text()).toContain('first_frame')
    await wrapper.findAll('button').find(button => button.text().includes('apiDocs.copyMarkdown'))!.trigger('click')
    expect(copyToClipboard).toHaveBeenLastCalledWith(expect.stringContaining('"aspect_ratio": "adaptive"'))
    expect(copyToClipboard).toHaveBeenLastCalledWith(expect.stringContaining('创建结果未知时先查原任务'))
    await router.push('/docs/kemei-seedance25-reference')
    await flushPromises()
    expect(wrapper.get('article').text()).toContain('reference_image')
    expect(wrapper.get('article').text()).toContain('"aspect_ratio": "16:9"')
    wrapper.unmount()
  })

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
    // 搜索和筛选自动展开所有匹配的分类。
    await wrapper.get('input[placeholder="apiDocs.search"]').setValue('video')
    await flushPromises()
    const videoCatalog = wrapper.get('nav[aria-label="视频与异步任务"]')
    // 模型指南复用已注册路径；目录按协议/模型家族分组，不能将指南数量误作新增服务端路由。
    expect(videoCatalog.findAll('h3').map(heading => heading.text())).toEqual([
      'OpenAI Videos5', 'Seedance / 火山方舟7', 'Kling / 可灵3', 'Wan / 万相1', 'MiniMax2', 'Vidu4', 'Grok / xAI5',
    ])
    expect(videoCatalog.findAll('a')).toHaveLength(27)
    await wrapper.get('input[placeholder="apiDocs.search"]').setValue('Vidu')
    await flushPromises()
    // 统一入口与迁移页的说明提到 Vidu 旧路径，同样命中。
    expect(videoCatalog.findAll('h3').map(heading => heading.text())).toEqual(['OpenAI Videos3', 'Vidu4'])
    expect(videoCatalog.findAll('a')).toHaveLength(7)
    await wrapper.get('input[placeholder="apiDocs.search"]').setValue('Kling')
    await flushPromises()
    // 视频检索包含使用说明，旧入口迁移页中对 Kling 旧路径的说明也应命中。
    expect(videoCatalog.findAll('h3').map(heading => heading.text())).toEqual(['OpenAI Videos3', 'Kling / 可灵3'])
    expect(videoCatalog.find('a[href="/docs/video-retired-endpoints"]').exists()).toBe(true)
    expect(wrapper.find('article').text()).toContain('Kling · 图生视频')
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

  // 管理员可改名、移动分类、隐藏和新增文档；隐藏的文档在目录和地址中都不可见。
  it('按目录叠加改名、排序、隐藏并显示自定义文档', async () => {
    getCatalog.mockResolvedValue({
      categories: [
        { id: 'faq', title: '常见问题', sort: 1, hidden: false, custom: true, version: 1, updated_at: '' },
        { id: 'audio', title: null, sort: null, hidden: true, custom: false, version: 1, updated_at: '' },
      ],
      documents: [
        docOverlay('responses', { title: '响应接口（改名）', category_id: 'faq' }),
        docOverlay('auth', { hidden: true }),
        docOverlay('faq-billing', { title: '计费问答', category_id: 'faq', has_markdown: true, custom: true }),
      ],
    })
    getDocument.mockResolvedValue(docOverlay('faq-billing', { markdown: '# 计费问答\n\n按量计费。', custom: true }))
    const { wrapper, router } = await render('/docs/faq-billing')
    const navs = wrapper.findAll('nav[aria-label]').map(nav => nav.attributes('aria-label'))
    expect(navs[0]).toBe('常见问题')
    expect(navs).not.toContain('语音与实时会话')
    const faq = wrapper.get('nav[aria-label="常见问题"]')
    expect(faq.text()).toContain('响应接口（改名）')
    expect(faq.text()).toContain('计费问答')
    expect(wrapper.find('a[href="/docs/auth"]').exists()).toBe(false)
    expect(wrapper.get('article').text()).toContain('按量计费。')
    await router.push('/docs/auth')
    await flushPromises()
    expect(wrapper.get('article').text()).toContain('apiDocs.notFound')
    wrapper.unmount()
  })

  // 二级标题（视频协议）可改名、删除；删除后其下文档在目录和地址中都不可见。
  it('二级标题按后台设置改名与删除', async () => {
    getCatalog.mockResolvedValue({
      categories: [
        { id: 'video-openai', parent_id: null, title: 'OpenAI 视频接口', sort: null, hidden: false, custom: false, version: 1, updated_at: '' },
        { id: 'video-vidu', parent_id: null, title: null, sort: null, hidden: true, custom: false, version: 1, updated_at: '' },
      ],
      documents: [],
    })
    const { wrapper, router } = await render('/docs/video-create')
    // 搜索自动展开匹配的分类。
    await wrapper.get('input[placeholder="apiDocs.search"]').setValue('video')
    await flushPromises()
    const videoCatalog = wrapper.get('nav[aria-label="视频与异步任务"]')
    const headings = videoCatalog.findAll('h3').map(heading => heading.text())
    expect(headings[0]).toBe('OpenAI 视频接口5')
    expect(headings.some(heading => heading.startsWith('Vidu'))).toBe(false)
    const viduDoc = wrapper.find('a[href^="/docs/vidu"]')
    expect(viduDoc.exists()).toBe(false)
    await wrapper.get('input[placeholder="apiDocs.search"]').setValue('')
    await router.push('/docs/video-create')
    await flushPromises()
    // 面包屑同时显示一级与二级标题。
    expect(wrapper.get('article').text()).toContain('视频与异步任务')
    expect(wrapper.get('article').text()).toContain('OpenAI 视频接口')
    wrapper.unmount()
  })

  it('移动目录能展开，选择接口后自动收起', async () => {
    const { wrapper } = await render('/docs/quickstart')
    const trigger = wrapper.get('button[aria-controls="api-doc-catalog"]')
    expect(trigger.attributes('aria-expanded')).toBe('false')
    await trigger.trigger('click')
    expect(trigger.attributes('aria-expanded')).toBe('true')
    // 点击"对话与生成"分类标题展开该分类。
    const navs = wrapper.findAll('nav[aria-label]')
    const textIndex = navs.findIndex(nav => nav.attributes('aria-label')?.startsWith('对话与生成'))
    await navs[textIndex].find('button').trigger('click')
    await flushPromises()
    // 选择接口后目录自动收起。
    await wrapper.get('a[href="/docs/responses"]').trigger('click')
    await flushPromises()
    expect(trigger.attributes('aria-expanded')).toBe('false')
    wrapper.unmount()
  })

  // 目录默认折叠，点击一级标题展开，再次点击折叠；选中文档时自动展开所属分类。
  it('目录默认折叠，点击展开/折叠，选中文档自动展开所属分类', async () => {
    const { wrapper, router } = await render('/docs/overview')
    // 初始状态：overview 所属的"指南"分类自动展开。
    expect(wrapper.findAll('nav[aria-label]')[0].attributes('aria-label')).toBe('指南')
    expect(wrapper.findAll('nav[aria-label]')[0].find('a[href="/docs/quickstart"]').isVisible()).toBe(true)
    // 找到"对话与生成"分类并确认默认折叠。
    let navs = wrapper.findAll('nav[aria-label]')
    let textIndex = navs.findIndex(nav => nav.attributes('aria-label')?.startsWith('对话与生成'))
    expect(textIndex).toBeGreaterThan(0)
    expect(navs[textIndex].find('a[href="/docs/messages"]').exists()).toBe(false)
    // 点击展开。
    await navs[textIndex].find('button').trigger('click')
    await flushPromises()
    navs = wrapper.findAll('nav[aria-label]')
    textIndex = navs.findIndex(nav => nav.attributes('aria-label')?.startsWith('对话与生成'))
    expect(navs[textIndex].find('a[href="/docs/messages"]').isVisible()).toBe(true)
    // 再次点击折叠。
    await navs[textIndex].find('button').trigger('click')
    await flushPromises()
    navs = wrapper.findAll('nav[aria-label]')
    textIndex = navs.findIndex(nav => nav.attributes('aria-label')?.startsWith('对话与生成'))
    expect(navs[textIndex].find('a[href="/docs/messages"]').exists()).toBe(false)
    // 导航到"视频与异步任务"下的文档，自动展开该分类。
    await router.push('/docs/video-create')
    await flushPromises()
    navs = wrapper.findAll('nav[aria-label]')
    const videoIndex = navs.findIndex(nav => nav.attributes('aria-label')?.startsWith('视频与异步任务'))
    expect(videoIndex).toBeGreaterThan(0)
    expect(navs[videoIndex].find('a[href="/docs/video-create"]').isVisible()).toBe(true)
    wrapper.unmount()
  })
})
