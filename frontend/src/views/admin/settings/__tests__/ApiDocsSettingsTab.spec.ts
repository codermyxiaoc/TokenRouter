import { mount, flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ApiDocsSettingsTab from '../ApiDocsSettingsTab.vue'

const api = vi.hoisted(() => ({
  getAdminCatalog: vi.fn(), getAdminDocument: vi.fn(), saveDocument: vi.fn(), deleteDocument: vi.fn(),
  saveCategory: vi.fn(), deleteCategory: vi.fn(), reorder: vi.fn(),
}))
const { showSuccess } = vi.hoisted(() => ({ showSuccess: vi.fn() }))
vi.mock('@/api/apiDocs', () => ({ apiDocsAPI: api }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ apiBaseUrl: 'https://gateway.example.com/v1', showSuccess }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key }),
}))

const docOverlay = (id: string, extra: Record<string, unknown> = {}) => ({
  id, category_id: null, title: null, has_markdown: false, sort: null, hidden: false, custom: false, version: 1, updated_at: '', ...extra,
})

// BaseDialog 只在打开时渲染插槽，避免测试依赖 Teleport。
const stubs = {
  Icon: true, ApiDocMarkdown: true, Select: true, Toggle: true, SearchInput: true,
  BaseDialog: { props: ['show', 'title'], template: '<div v-if="show" data-testid="dialog"><slot /><slot name="footer" /></div>' },
  ConfirmDialog: { props: ['show', 'title', 'message'], emits: ['confirm', 'cancel'], template: '<div v-if="show" data-testid="confirm"><button data-testid="confirm-ok" @click="$emit(\'confirm\')">ok</button></div>' },
}

async function render() {
  const wrapper = mount(ApiDocsSettingsTab, { global: { stubs } })
  await flushPromises()
  return wrapper
}

describe('API 文档目录管理', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.getAdminCatalog.mockResolvedValue({ categories: [], documents: [docOverlay('quickstart', { has_markdown: true })] })
    api.getAdminDocument.mockResolvedValue(docOverlay('quickstart', { markdown: '# 旧正文' }))
    api.reorder.mockResolvedValue(undefined)
  })

  it('内置文档只提交与内置不同的字段并携带版本', async () => {
    api.saveDocument.mockResolvedValue(docOverlay('quickstart', { version: 2 }))
    const wrapper = await render()
    expect((wrapper.get('[data-testid="api-docs-editor"]').element as HTMLTextAreaElement).value).toBe('# 旧正文')
    await wrapper.get('[data-testid="api-docs-title"]').setValue('开始使用')
    await wrapper.get('[data-testid="api-docs-editor"]').setValue('# 新正文')
    await wrapper.get('[data-testid="api-docs-save"]').trigger('click')
    await flushPromises()
    expect(api.saveDocument).toHaveBeenCalledWith('quickstart', {
      title: '开始使用', category_id: null, markdown: '# 新正文', hidden: false, custom: false, version: 1,
    })
    expect(showSuccess).toHaveBeenCalled()
  })

  it('他人已修改时提示冲突并保留草稿', async () => {
    api.saveDocument.mockRejectedValue({ reason: 'API_DOC_CONFLICT', status: 409 })
    const wrapper = await render()
    await wrapper.get('[data-testid="api-docs-editor"]').setValue('# 我的修改')
    await wrapper.get('[data-testid="api-docs-save"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('admin.apiDocsSettings.conflict')
    expect((wrapper.get('[data-testid="api-docs-editor"]').element as HTMLTextAreaElement).value).toBe('# 我的修改')
    expect(api.getAdminCatalog).toHaveBeenCalledTimes(2)
  })

  it('新增自定义文档：校验 ID 并以 version=0 创建', async () => {
    api.saveDocument.mockResolvedValue(docOverlay('my-guide', { custom: true }))
    const wrapper = await render()
    await wrapper.get('[data-testid="api-docs-add-document"]').trigger('click')
    await wrapper.get('[data-testid="api-docs-new-title"]').setValue('My Guide')
    expect((wrapper.get('[data-testid="api-docs-new-id"]').element as HTMLInputElement).value).toBe('my-guide')
    // 内置 ID 不可占用。
    await wrapper.get('[data-testid="api-docs-new-id"]').setValue('quickstart')
    await wrapper.get('form#api-doc-create-form').trigger('submit')
    await flushPromises()
    expect(api.saveDocument).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('admin.apiDocsSettings.idTaken')
    await wrapper.get('[data-testid="api-docs-new-id"]').setValue('my-guide')
    await wrapper.get('form#api-doc-create-form').trigger('submit')
    await flushPromises()
    expect(api.saveDocument).toHaveBeenCalledWith('my-guide', expect.objectContaining({ title: 'My Guide', custom: true, version: 0, markdown: '# My Guide\n' }))
  })

  it('删除自定义文档需二次确认并携带版本', async () => {
    api.getAdminCatalog.mockResolvedValue({ categories: [], documents: [
      docOverlay('my-guide', { custom: true, title: '自定义', category_id: 'guides', has_markdown: true, version: 3 }),
    ] })
    api.getAdminDocument.mockResolvedValue(docOverlay('my-guide', { custom: true, title: '自定义', category_id: 'guides', markdown: '# x', version: 3 }))
    api.deleteDocument.mockResolvedValue(undefined)
    const wrapper = await render()
    await wrapper.get('[data-testid="api-docs-doc-my-guide"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="api-docs-delete"]').trigger('click')
    expect(api.deleteDocument).not.toHaveBeenCalled()
    await wrapper.get('[data-testid="confirm-ok"]').trigger('click')
    await flushPromises()
    expect(api.deleteDocument).toHaveBeenCalledWith('my-guide', 3)
  })

  it('上下移动提交该分类的新顺序', async () => {
    const wrapper = await render()
    const guides = wrapper.get('[data-testid="api-docs-category-guides"]')
    // 第二篇（auth）上移到首位。
    const buttons = guides.findAll('button[aria-label="admin.apiDocsSettings.moveUp"]')
    await buttons[2].trigger('click')
    await flushPromises()
    expect(api.reorder).toHaveBeenCalledWith({ documents: { guides: ['auth', 'quickstart', 'overview', 'errors'] } })
  })

  it('新增分类时自动生成 ID 并标记为自定义', async () => {
    api.saveCategory.mockResolvedValue({ id: 'faq', title: 'FAQ', sort: null, hidden: false, custom: true, version: 1, updated_at: '' })
    const wrapper = await render()
    await wrapper.get('[data-testid="api-docs-add-category"]').trigger('click')
    await wrapper.get('[data-testid="api-docs-category-title"]').setValue('FAQ')
    await wrapper.get('form#api-doc-category-form').trigger('submit')
    await flushPromises()
    expect(api.saveCategory).toHaveBeenCalledWith('faq', { title: 'FAQ', hidden: false, custom: true, version: 0 })
  })
})
