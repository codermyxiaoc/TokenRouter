import { mount, flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ApiDocsSettingsTab from '../ApiDocsSettingsTab.vue'

const { getOverrides, patchOverrides, updateOverrides, showSuccess } = vi.hoisted(() => ({
  getOverrides: vi.fn(), patchOverrides: vi.fn(), updateOverrides: vi.fn(), showSuccess: vi.fn(),
}))
vi.mock('@/api/apiDocs', () => ({ apiDocsAPI: { getOverrides, patchOverrides, updateOverrides } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ apiBaseUrl: 'https://gateway.example.com/v1', showSuccess }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key }),
}))

async function render() {
  const wrapper = mount(ApiDocsSettingsTab, { global: { stubs: { Icon: true, ApiDocMarkdown: true, Select: true } } })
  await flushPromises()
  return wrapper
}

// API 文档编辑：只提交当前文档并携带编辑起点，避免多名管理员互相覆盖。
describe('API 文档设置', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    getOverrides.mockResolvedValue({ quickstart: '# 旧正文', other: '# 他人的文档' })
  })

  it('保存只提交当前文档与编辑起点，不整体替换其它文档', async () => {
    patchOverrides.mockResolvedValue({ quickstart: '# 新正文', other: '# 他人的文档' })
    const wrapper = await render()
    await wrapper.get('[data-testid="api-docs-editor"]').setValue('# 新正文')
    await wrapper.get('[data-testid="api-docs-save"]').trigger('click')
    await flushPromises()
    expect(patchOverrides).toHaveBeenCalledWith({ quickstart: { markdown: '# 新正文', base: '# 旧正文' } })
    expect(updateOverrides).not.toHaveBeenCalled()
    expect(showSuccess).toHaveBeenCalled()
  })

  it('同一文档已被他人修改时提示冲突并保留草稿', async () => {
    patchOverrides.mockRejectedValue({ reason: 'API_DOC_CONFLICT', status: 409 })
    const wrapper = await render()
    await wrapper.get('[data-testid="api-docs-editor"]').setValue('# 我的修改')
    await wrapper.get('[data-testid="api-docs-save"]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('admin.apiDocsSettings.conflict')
    expect((wrapper.get('[data-testid="api-docs-editor"]').element as HTMLTextAreaElement).value).toBe('# 我的修改')
    expect(getOverrides).toHaveBeenCalledTimes(2)
  })

  it('保存期间继续编辑的内容不会被响应覆盖', async () => {
    let resolve: (value: Record<string, string>) => void = () => {}
    patchOverrides.mockReturnValue(new Promise(r => { resolve = r }))
    const wrapper = await render()
    const editor = wrapper.get('[data-testid="api-docs-editor"]')
    await editor.setValue('# 第一版')
    await wrapper.get('[data-testid="api-docs-save"]').trigger('click')
    await editor.setValue('# 保存期间继续输入')
    resolve({ quickstart: '# 第一版', other: '# 他人的文档' })
    await flushPromises()
    expect((editor.element as HTMLTextAreaElement).value).toBe('# 保存期间继续输入')
    expect(wrapper.text()).toContain('admin.apiDocsSettings.unsaved')
  })
})
