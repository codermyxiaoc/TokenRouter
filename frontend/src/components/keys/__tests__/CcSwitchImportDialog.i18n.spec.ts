import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ApiKey, MarketplaceGroup } from '@/types'

// 与 Vite 保持相同 JIT 模式，使用真实语言包及翻译器，不能以返回键名的 mock 验证文案。
vi.hoisted(() => {
  vi.stubGlobal('__INTLIFY_JIT_COMPILATION__', true)
})

import i18n, { loadLocaleMessages } from '@/i18n'
import { getMarketplaceModels } from '@/api/marketplace'
import CcSwitchImportDialog from '../CcSwitchImportDialog.vue'

// 仅替换目录请求，保留正式语言包入口、翻译器和真实模型选择器。
vi.mock('@/api/marketplace', () => ({ getMarketplaceModels: vi.fn() }))

const marketplaceModels = [{
  id: 1,
  name: 'Public models',
  platform: 'anthropic',
  models: [{ id: 'claude-sonnet-4-5', display_name: 'Claude Sonnet 4.5' }]
}] as MarketplaceGroup[]

const localeCopies = [
  {
    locale: 'zh' as const,
    title: '填入 CC Switch',
    model: '主模型',
    placeholder: '请选择模型',
    searchPlaceholder: '搜索模型名称或 ID',
    namePlaceholder: '请输入配置名称',
    nameRequired: '请输入配置名称。',
    modelRequired: '请选择主模型。',
    loading: '正在加载网站模型…',
    failed: '网站模型加载失败，请重试。',
    retry: '重新加载',
    empty: '网站暂无可选模型。'
  },
  {
    locale: 'en' as const,
    title: 'Import to CC Switch',
    model: 'Main model',
    placeholder: 'Select a model',
    searchPlaceholder: 'Search model name or ID',
    namePlaceholder: 'Enter a configuration name',
    nameRequired: 'Enter a configuration name.',
    modelRequired: 'Select a main model.',
    loading: 'Loading site models…',
    failed: 'Could not load site models. Please retry.',
    retry: 'Reload',
    empty: 'No models are available on this site.'
  }
]

async function mountDialog(locale: 'zh' | 'en') {
  await loadLocaleMessages(locale)
  i18n.global.locale.value = locale
  return mount(CcSwitchImportDialog, {
    props: {
      show: true,
      apiKey: { id: 1, name: 'Test key', key: 'sk-test-only', group: { platform: 'anthropic' } } as ApiKey
    },
    global: {
      plugins: [i18n],
      stubs: {
        teleport: true,
        BaseDialog: {
          props: ['show', 'title'],
          template: '<div v-if="show"><h3>{{ title }}</h3><slot /><slot name="footer" /></div>'
        }
      }
    }
  })
}

describe.each(localeCopies)('CcSwitchImportDialog real $locale locale integration', (copy) => {
  beforeEach(() => {
    vi.mocked(getMarketplaceModels).mockReset().mockResolvedValue(marketplaceModels)
  })

  it('renders selection, search and required-field copy through the complete locale entry', async () => {
    const wrapper = await mountDialog(copy.locale)
    try {
      await flushPromises()
      expect(getMarketplaceModels).toHaveBeenCalledWith(expect.any(AbortSignal))
      expect(wrapper.get('h3').text()).toBe(copy.title)
      expect(wrapper.get<HTMLInputElement>('input#ccs-provider-name').element.value).toBe('My Claude')
      expect(wrapper.get('#ccs-provider-name').attributes('placeholder')).toBe(copy.namePlaceholder)
      expect(wrapper.get('#ccs-main-model').attributes('aria-label')).toBe(copy.model)
      expect(wrapper.get('#ccs-main-model').text()).toBe(copy.placeholder)
      expect(wrapper.text()).toContain('Haiku')

      await wrapper.get('#ccs-main-model').trigger('click')
      expect(wrapper.get('.select-search-input').attributes('placeholder')).toBe(copy.searchPlaceholder)
      expect(wrapper.get('[role="option"]').text()).toBe('claude-sonnet-4-5')
      expect(wrapper.html()).not.toContain('keys.ccsImport.')
      await wrapper.get('[role="listbox"]').trigger('keydown', { key: 'Escape' })

      await wrapper.get('form').trigger('submit')
      expect(wrapper.get('[role="alert"]').text()).toBe(copy.modelRequired)
      await wrapper.get('#ccs-provider-name').setValue('')
      expect(wrapper.get('[role="alert"]').text()).toBe(copy.nameRequired)
      expect(wrapper.html()).not.toContain('keys.ccsImport.')
      expect(wrapper.emitted('confirm')).toBeUndefined()
    } finally {
      wrapper.unmount()
    }
  })

  it('renders loading, failure, retry and empty-directory copy without raw message keys', async () => {
    let rejectModels!: (reason: Error) => void
    vi.mocked(getMarketplaceModels).mockImplementationOnce(() => new Promise((_, reject) => {
      rejectModels = reject
    }))
    const wrapper = await mountDialog(copy.locale)
    try {
      expect(wrapper.get('[role="status"]').text()).toBe(copy.loading)
      expect(wrapper.get('#ccs-main-model').text()).toBe(copy.loading)
      expect(wrapper.html()).not.toContain('keys.ccsImport.')

      rejectModels(new Error('Test directory failure'))
      await flushPromises()
      expect(wrapper.get('[role="status"]').text()).toBe(copy.failed)
      expect(wrapper.get('[data-testid="ccs-retry-models"]').text()).toBe(copy.retry)
      expect(wrapper.html()).not.toContain('keys.ccsImport.')

      vi.mocked(getMarketplaceModels).mockResolvedValueOnce([])
      await wrapper.get('[data-testid="ccs-retry-models"]').trigger('click')
      await flushPromises()
      expect(getMarketplaceModels).toHaveBeenCalledTimes(2)
      expect(wrapper.get('[role="status"]').text()).toBe(copy.empty)
      expect(wrapper.get('#ccs-main-model').text()).toBe(copy.placeholder)
      expect(wrapper.html()).not.toContain('keys.ccsImport.')
      expect(wrapper.get('[data-testid="ccs-confirm"]').attributes('disabled')).toBeDefined()
    } finally {
      wrapper.unmount()
    }
  })
})
