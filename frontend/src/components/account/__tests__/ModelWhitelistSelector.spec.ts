import { describe, expect, it, vi, beforeEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import ModelWhitelistSelector from '../ModelWhitelistSelector.vue'

const {
  syncUpstreamModels,
  syncUpstreamModelsPreview,
  showError,
  showInfo,
  showSuccess,
  copyToClipboard
} = vi.hoisted(() => ({
  syncUpstreamModels: vi.fn(),
  syncUpstreamModelsPreview: vi.fn(),
  showError: vi.fn(),
  showInfo: vi.fn(),
  showSuccess: vi.fn(),
  copyToClipboard: vi.fn().mockResolvedValue(true)
}))

vi.mock('@/api/admin/accounts', () => ({
  accountsAPI: {
    syncUpstreamModels,
    syncUpstreamModelsPreview
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError,
    showInfo,
    showSuccess
  })
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copyToClipboard })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => {
        if (key === 'admin.accounts.syncUpstreamModelsFailed') return '同步上游模型失败'
        if (key === 'admin.accounts.syncUpstreamModelsError') return `同步上游模型失败：${params?.message}`
        return params ? `${key}:${JSON.stringify(params)}` : key
      }
    })
  }
})

function mountSelector(props: Record<string, unknown> = {}) {
  return mount(ModelWhitelistSelector, {
    props: {
      modelValue: [],
      platform: 'openai',
      ...props
    },
    global: {
      stubs: {
        ModelIcon: true,
        Icon: true
      }
    }
  })
}

describe('ModelWhitelistSelector', () => {
  it.each([
    ['openai', 'gpt-6.1-sol'], ['anthropic', 'claude-sonnet-5-5']
  ])('展示并选择 %s 新型号 %s', async (platform, model) => {
    const wrapper = mountSelector({ platform })
    await wrapper.get('div.cursor-pointer').trigger('click')
    const row = wrapper.findAll('[data-testid="model-option"]').find(candidate => candidate.text().includes(model))
    expect(row).toBeTruthy()
    await row!.get('[data-testid="select-model"]').trigger('click')
    expect(wrapper.emitted('update:modelValue')).toEqual([[[model]]])
  })

  beforeEach(() => {
    syncUpstreamModels.mockReset()
    syncUpstreamModelsPreview.mockReset()
    showError.mockReset()
    showInfo.mockReset()
    showSuccess.mockReset()
    copyToClipboard.mockReset()
    copyToClipboard.mockResolvedValue(true)
  })

  it('Video only permits explicit models and hides upstream text model discovery', () => {
    const wrapper = mountSelector({ platform: 'video', accountId: 7 })
    expect(wrapper.text()).not.toContain('admin.accounts.fillRelatedModels')
    expect(wrapper.text()).not.toContain('admin.accounts.syncUpstreamModels')
    expect(wrapper.findAll('[data-testid="model-option"]')).toHaveLength(0)
    wrapper.unmount()
  })

  it('复制模型 ID 时不会选中模型', async () => {
    const wrapper = mountSelector()
    await wrapper.get('div.cursor-pointer').trigger('click')

    const row = wrapper
      .findAll('[data-testid="model-option"]')
      .find(candidate => candidate.text().includes('gpt-5.6-sol'))
    expect(row).toBeTruthy()

    const copyButton = row!.get('[data-testid="copy-model-id"]')
    expect(copyButton.attributes('aria-label')).toBe('common.copy gpt-5.6-sol')
    await copyButton.trigger('click')
    await flushPromises()

    expect(copyToClipboard).toHaveBeenCalledWith('gpt-5.6-sol')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })

  it('模型选择行为保持不变', async () => {
    const wrapper = mountSelector()
    await wrapper.get('div.cursor-pointer').trigger('click')

    const row = wrapper
      .findAll('[data-testid="model-option"]')
      .find(candidate => candidate.text().includes('gpt-5.6-sol'))
    expect(row).toBeTruthy()
    await row!.get('[data-testid="select-model"]').trigger('click')

    expect(wrapper.emitted('update:modelValue')).toEqual([[['gpt-5.6-sol']]])
    expect(copyToClipboard).not.toHaveBeenCalled()
  })

  it('创建账号时使用临时凭证同步上游模型', async () => {
    syncUpstreamModelsPreview.mockResolvedValue({ models: ['gpt-5.1', 'o3', 'gpt-5.1'] })
    const syncCredentials = {
      platform: 'openai',
      type: 'apikey',
      base_url: 'https://openai.example.com/v1',
      api_key: 'openai-key'
    }
    const wrapper = mountSelector({ syncCredentials })

    const button = wrapper.findAll('button').find((item) => item.text().includes('admin.accounts.syncUpstreamModels'))
    expect(button).toBeTruthy()
    await button!.trigger('click')

    expect(syncUpstreamModelsPreview).toHaveBeenCalledWith(syncCredentials)
    expect(syncUpstreamModels).not.toHaveBeenCalled()
    expect(wrapper.emitted('update:modelValue')?.[0]?.[0]).toEqual(['gpt-5.1', 'o3'])
    expect(showSuccess).toHaveBeenCalled()
  })

  it('编辑账号时仍使用账号 ID 同步上游模型', async () => {
    syncUpstreamModels.mockResolvedValue({ models: ['claude-sonnet-4-5'] })
    const wrapper = mountSelector({
      platform: 'anthropic',
      accountId: 7,
      syncCredentials: {
        platform: 'anthropic',
        type: 'apikey',
        api_key: 'should-not-use'
      }
    })

    const button = wrapper.findAll('button').find((item) => item.text().includes('admin.accounts.syncUpstreamModels'))
    expect(button).toBeTruthy()
    await button!.trigger('click')

    expect(syncUpstreamModels).toHaveBeenCalledWith(7)
    expect(syncUpstreamModelsPreview).not.toHaveBeenCalled()
    expect(wrapper.emitted('update:modelValue')?.[0]?.[0]).toEqual(['claude-sonnet-4-5'])
  })

  it.each([
    ['客户端普通错误对象', { status: 502, message: '上游模型接口返回 HTTP 403（Cloudflare）' }, '上游模型接口返回 HTTP 403（Cloudflare）'],
    ['Axios 错误', { message: 'Request failed with status code 502', response: { data: { message: '上游模型接口返回 HTTP 403（Cloudflare）' } } }, '上游模型接口返回 HTTP 403（Cloudflare）'],
    ['旧版 detail 字段', { response: { data: { detail: '上游未开放模型列表接口' } } }, '上游未开放模型列表接口'],
    ['附带安全详情', { message: '同步上游模型失败', details: '上游模型接口返回 HTTP 403' }, '同步上游模型失败: 上游模型接口返回 HTTP 403'],
    ['详情已经包含在说明中', { message: '同步上游模型失败: HTTP 403', details: 'HTTP 403' }, '同步上游模型失败: HTTP 403'],
    ['标准 Error', new Error('连接超时，请重试'), '连接超时，请重试'],
    ['缺少错误说明', { status: 502 }, '同步上游模型失败'],
    ['HTML 错误页', { response: { data: '<html><body>secret-key</body></html>' }, config: { headers: { Authorization: 'secret-key' } } }, '同步上游模型失败'],
    ['HTML 说明字段', { message: '<html>secret-key</html>', details: { api_key: 'secret-key' } }, '同步上游模型失败']
  ])('Gemini 同步失败时正确展示%s，且允许重试', async (_name, error, expectedMessage) => {
    syncUpstreamModelsPreview.mockRejectedValueOnce(error).mockResolvedValueOnce({ models: ['gemini-2.5-pro'] })
    const syncCredentials = {
      platform: 'gemini',
      type: 'apikey',
      provider_type: 'third_party',
      base_url: 'https://gemini.example.test',
      api_key: 'provider-key'
    }
    const wrapper = mountSelector({ platform: 'gemini', modelValue: ['existing-model'], syncCredentials })
    const button = wrapper.findAll('button').find(item => item.text().includes('admin.accounts.syncUpstreamModels'))!

    await button.trigger('click')
    await flushPromises()

    expect(syncUpstreamModelsPreview).toHaveBeenCalledWith(syncCredentials)
    expect(showError).toHaveBeenCalledTimes(1)
    expect(showError).toHaveBeenCalledWith(expectedMessage)
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    expect(button.attributes('disabled')).toBeUndefined()

    await button.trigger('click')
    await flushPromises()

    expect(wrapper.emitted('update:modelValue')?.[0]?.[0]).toEqual(['existing-model', 'gemini-2.5-pro'])
    wrapper.unmount()
  })

  it('编辑账号同步失败时保留客户端归一化后的错误说明', async () => {
    syncUpstreamModels.mockRejectedValue({ status: 502, message: '上游模型接口返回 HTTP 404' })
    const wrapper = mountSelector({ platform: 'gemini', accountId: 7 })
    const button = wrapper.findAll('button').find(item => item.text().includes('admin.accounts.syncUpstreamModels'))!

    await button.trigger('click')
    await flushPromises()

    expect(syncUpstreamModels).toHaveBeenCalledWith(7)
    expect(showError).toHaveBeenCalledTimes(1)
    expect(showError).toHaveBeenCalledWith('上游模型接口返回 HTTP 404')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    wrapper.unmount()
  })
})
