import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ApiKey, MarketplaceGroup } from '@/types'
import { getMarketplaceModels } from '@/api/marketplace'
import Select from '@/components/common/Select.vue'
import CcSwitchImportDialog from '../CcSwitchImportDialog.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/api/marketplace', () => ({ getMarketplaceModels: vi.fn() }))

const fetchModels = vi.mocked(getMarketplaceModels)
const key = {
  id: 1, key: 'sk-test-only', name: 'Laptop', group: { platform: 'openai' }
} as ApiKey
const wrappers = new Set<VueWrapper>()

// 分组中故意包含重复 ID、大小写不同的 ID 和跨平台模型，验证全站目录不按应用缩小。
function marketplaceFixture(): MarketplaceGroup[] {
  return [
    {
      id: 1, platform: 'anthropic', models: [
        { id: 'claude-main', display_name: '旗舰对话模型' },
        { id: 'claude-haiku', display_name: '轻量模型' },
        { id: 'claude-sonnet', display_name: '均衡模型' },
        { id: 'claude-opus', display_name: '深度模型' }
      ]
    },
    {
      id: 2, platform: 'openai', models: [
        { id: 'gpt-5.6-sol', display_name: 'Code flagship' },
        { id: 'claude-main', display_name: '重复展示名' },
        { id: 'CLAUDE-MAIN', display_name: '另一个精确 ID' }
      ]
    },
    { id: 3, platform: 'gemini', models: [{ id: 'Gemini/Flash', display_name: '极速模型' }] }
  ] as MarketplaceGroup[]
}
const modelIds = ['claude-main', 'claude-haiku', 'claude-sonnet', 'claude-opus', 'gpt-5.6-sol', 'CLAUDE-MAIN', 'Gemini/Flash']

// 只替换弹窗布局与 Teleport 容器，模型选择经过真实 Select 的搜索和选项事件。
function mountDialog(show = true, apiKey: ApiKey | null = key) {
  const wrapper = mount(CcSwitchImportDialog, {
    props: { show, apiKey },
    global: { stubs: {
      teleport: true,
      BaseDialog: {
        props: ['show', 'title'], emits: ['close'],
        template: '<div v-if="show"><button data-testid="dialog-close" @click="$emit(\'close\')">close</button><slot /><slot name="footer" /></div>'
      }
    } }
  })
  wrappers.add(wrapper)
  return wrapper
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

async function selectModel(wrapper: VueWrapper, field: string, id: string, search = id) {
  await wrapper.get('#ccs-' + field).trigger('click')
  await wrapper.get('.select-search-input').setValue(search)
  const option = wrapper.findAll('[role="option"]').find((item) => item.text() === id)
  expect(option, '模型应可通过 ID 或显示名称找到：' + id).toBeDefined()
  await option!.trigger('click')
}

async function submit(wrapper: VueWrapper) {
  await wrapper.get('form').trigger('submit')
  await flushPromises()
}

describe('CcSwitchImportDialog', () => {
  beforeEach(() => {
    fetchModels.mockReset().mockResolvedValue(marketplaceFixture())
  })

  afterEach(() => {
    for (const wrapper of wrappers) wrapper.unmount()
    wrappers.clear()
  })

  it('loads the website catalog only when opened with a key and waits for confirmation', async () => {
    const wrapper = mountDialog(false)
    expect(wrapper.find('form').exists()).toBe(false)
    expect(fetchModels).not.toHaveBeenCalled()
    await wrapper.setProps({ show: true, apiKey: null })
    expect(fetchModels).not.toHaveBeenCalled()
    expect(wrapper.get('[data-testid="ccs-confirm"]').attributes('disabled')).toBeDefined()
    await wrapper.setProps({ apiKey: key })
    await flushPromises()
    expect(fetchModels).toHaveBeenCalledTimes(1)
    expect(fetchModels).toHaveBeenCalledWith(expect.any(AbortSignal))
    expect((wrapper.get('input[value="codex"]').element as HTMLInputElement).checked).toBe(true)
    expect(wrapper.get('#ccs-provider-name').element).toBeInstanceOf(HTMLInputElement)
    expect((wrapper.get('#ccs-provider-name').element as HTMLInputElement).value).toBe('My Codex')
    expect(wrapper.get('#ccs-main-model').text()).toBe('keys.ccsImport.modelPlaceholder')
    expect(wrapper.emitted('confirm')).toBeUndefined()
    await wrapper.get('[data-testid="dialog-close"]').trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
    expect(wrapper.emitted('confirm')).toBeUndefined()
  })

  it('offers every website model in every app and Claude tier, deduplicating only exact IDs', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    for (const app of ['claude', 'codex', 'gemini']) {
      await wrapper.get('input[value="' + app + '"]').setValue()
      const selectors = wrapper.findAllComponents(Select)
      expect(selectors).toHaveLength(app === 'claude' ? 4 : 1)
      for (const selector of selectors) {
        expect(selector.props('searchable')).toBe(true)
        expect(selector.props('creatable')).toBe(false)
        expect(selector.props('options').map((option) => option.value)).toEqual(modelIds)
      }
      await wrapper.get('#ccs-main-model').trigger('click')
      expect(wrapper.findAll('[role="option"]').map((option) => option.text())).toEqual(modelIds)
      await wrapper.get('#ccs-main-model').trigger('click')
    }
    expect(fetchModels).toHaveBeenCalledTimes(1)
  })

  it('searches the real selector by ID and display name without accepting arbitrary entries', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    await selectModel(wrapper, 'main-model', 'claude-main', '旗舰对话')
    expect(wrapper.get('#ccs-main-model').text()).toContain('claude-main')
    await selectModel(wrapper, 'main-model', 'gpt-5.6-sol', 'GPT-5.6')
    await wrapper.get('#ccs-main-model').trigger('click')
    await wrapper.get('.select-search-input').setValue('not-in-the-website-catalog')
    expect(wrapper.findAll('[role="option"]')).toHaveLength(0)
    await wrapper.get('[role="listbox"]').trigger('keydown', { key: 'Enter' })
    expect(wrapper.get('#ccs-main-model').text()).toContain('gpt-5.6-sol')
    await wrapper.get('#ccs-main-model').trigger('click')
    await submit(wrapper)
    expect(wrapper.emitted('confirm')).toEqual([[{ app: 'codex', providerName: 'My Codex', model: 'gpt-5.6-sol' }]])
  })

  it('requires a name and main model and submits the chosen Claude models exactly', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    await wrapper.get('input[value="claude"]').setValue()
    await submit(wrapper)
    expect(wrapper.get('[role="alert"]').text()).toBe('keys.ccsImport.modelRequired')
    expect(wrapper.findAllComponents(Select)[0].props('error')).toBe(true)
    expect(wrapper.emitted('confirm')).toBeUndefined()
    await selectModel(wrapper, 'main-model', 'CLAUDE-MAIN')
    await wrapper.get('#ccs-provider-name').setValue(' ')
    await submit(wrapper)
    expect(wrapper.get('[role="alert"]').text()).toBe('keys.ccsImport.nameRequired')
    expect(wrapper.get('#ccs-provider-name').attributes('aria-invalid')).toBe('true')
    await wrapper.get('#ccs-provider-name').setValue(' 我的配置 ')
    await selectModel(wrapper, 'haikuModel', 'claude-haiku')
    await selectModel(wrapper, 'sonnetModel', 'claude-sonnet')
    await selectModel(wrapper, 'opusModel', 'claude-opus')
    await submit(wrapper)
    expect(wrapper.emitted('confirm')).toEqual([[{
      app: 'claude', providerName: '我的配置', model: 'CLAUDE-MAIN',
      haikuModel: 'claude-haiku', sonnetModel: 'claude-sonnet', opusModel: 'claude-opus'
    }]])
    await wrapper.get('#ccs-haikuModel .select-clear').trigger('click')
    await submit(wrapper)
    expect(wrapper.emitted('confirm')?.[1]).toEqual([{
      app: 'claude', providerName: '我的配置', model: 'CLAUDE-MAIN',
      sonnetModel: 'claude-sonnet', opusModel: 'claude-opus'
    }])
    await wrapper.get('#ccs-main-model .select-clear').trigger('click')
    await submit(wrapper)
    expect(wrapper.get('[role="alert"]').text()).toBe('keys.ccsImport.modelRequired')
    expect(wrapper.emitted('confirm')).toHaveLength(2)
  })

  it('keeps names and model drafts separate and excludes Claude tiers from Codex and Gemini', async () => {
    const wrapper = mountDialog()
    await flushPromises()
    await wrapper.get('input[value="claude"]').setValue()
    expect((wrapper.get('#ccs-provider-name').element as HTMLInputElement).value).toBe('My Claude')
    await wrapper.get('#ccs-provider-name').setValue('My custom Claude')
    await selectModel(wrapper, 'main-model', 'claude-main')
    await selectModel(wrapper, 'haikuModel', 'claude-haiku')
    for (const [app, label, model] of [['codex', 'Codex', 'gpt-5.6-sol'], ['gemini', 'Gemini', 'Gemini/Flash']]) {
      await wrapper.get('input[value="' + app + '"]').setValue()
      expect(wrapper.find('#ccs-haikuModel').exists()).toBe(false)
      expect((wrapper.get('#ccs-provider-name').element as HTMLInputElement).value).toBe('My ' + label)
      expect(wrapper.get('#ccs-main-model').text()).toBe('keys.ccsImport.modelPlaceholder')
      await wrapper.get('#ccs-provider-name').setValue('Custom ' + label)
      await selectModel(wrapper, 'main-model', model)
      await submit(wrapper)
    }
    expect(wrapper.emitted('confirm')).toEqual([
      [{ app: 'codex', providerName: 'Custom Codex', model: 'gpt-5.6-sol' }],
      [{ app: 'gemini', providerName: 'Custom Gemini', model: 'Gemini/Flash' }]
    ])
    await wrapper.get('input[value="claude"]').setValue()
    expect((wrapper.get('#ccs-provider-name').element as HTMLInputElement).value).toBe('My custom Claude')
    expect(wrapper.get('#ccs-haikuModel').text()).toContain('claude-haiku')
    expect(wrapper.get('#ccs-main-model').text()).toContain('claude-main')
    await wrapper.get('input[value="codex"]').setValue()
    expect((wrapper.get('#ccs-provider-name').element as HTMLInputElement).value).toBe('Custom Codex')
    expect(wrapper.get('#ccs-main-model').text()).toContain('gpt-5.6-sol')
  })

  it.each(['key change', 'close and reopen'])('resets all drafts and validation after %s', async (change) => {
    const wrapper = mountDialog()
    await flushPromises()
    await wrapper.get('input[value="claude"]').setValue()
    await wrapper.get('#ccs-provider-name').setValue('Discard this name')
    await selectModel(wrapper, 'main-model', 'claude-main')
    await selectModel(wrapper, 'haikuModel', 'claude-haiku')
    await wrapper.get('input[value="codex"]').setValue()
    await submit(wrapper)
    expect(wrapper.find('[role="alert"]').exists()).toBe(true)
    if (change === 'key change') {
      await wrapper.setProps({ apiKey: { ...key, id: 2, key: 'sk-second-only' } })
    } else {
      await wrapper.setProps({ show: false })
      await wrapper.setProps({ show: true })
    }
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.get('#ccs-main-model').text()).toBe('keys.ccsImport.modelPlaceholder')
    await wrapper.get('input[value="claude"]').setValue()
    expect((wrapper.get('#ccs-provider-name').element as HTMLInputElement).value).toBe('My Claude')
    expect(wrapper.get('#ccs-main-model').text()).toBe('keys.ccsImport.modelPlaceholder')
    expect(wrapper.get('#ccs-haikuModel').text()).toBe('keys.ccsImport.modelPlaceholder')
  })

  it('blocks import while loading, reports failure without raw details, and retries successfully', async () => {
    const pending = deferred<MarketplaceGroup[]>()
    fetchModels.mockReturnValueOnce(pending.promise)
    const wrapper = mountDialog()
    expect(wrapper.get('[role="status"]').text()).toBe('keys.ccsImport.loadingModels')
    expect(wrapper.get('#ccs-main-model').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="ccs-confirm"]').attributes('disabled')).toBeDefined()
    await submit(wrapper)
    expect(wrapper.emitted('confirm')).toBeUndefined()
    pending.reject(new Error('Private network response details'))
    await flushPromises()
    expect(wrapper.get('[role="status"]').text()).toBe('keys.ccsImport.modelsFailed')
    expect(wrapper.text()).not.toContain('Private network')
    expect(wrapper.get('[data-testid="ccs-confirm"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="ccs-retry-models"]').trigger('click')
    await flushPromises()
    expect(fetchModels).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[data-testid="ccs-retry-models"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="ccs-confirm"]').attributes('disabled')).toBeUndefined()
    await selectModel(wrapper, 'main-model', 'gpt-5.6-sol')
    await submit(wrapper)
    expect(wrapper.emitted('confirm')).toHaveLength(1)
  })

  it('shows an empty website catalog without inventing models or allowing import', async () => {
    fetchModels.mockResolvedValueOnce([])
    const wrapper = mountDialog()
    await flushPromises()
    expect(wrapper.get('[role="status"]').text()).toBe('keys.ccsImport.modelsEmpty')
    expect(wrapper.get('[data-testid="ccs-confirm"]').attributes('disabled')).toBeDefined()
    await wrapper.get('#ccs-main-model').trigger('click')
    expect(wrapper.findAll('[role="option"]')).toHaveLength(0)
    await submit(wrapper)
    expect(wrapper.emitted('confirm')).toBeUndefined()
  })

  it.each(['key change', 'close and reopen'])('aborts and discards stale successful responses after %s', async (change) => {
    const stale = deferred<MarketplaceGroup[]>()
    const current = deferred<MarketplaceGroup[]>()
    fetchModels.mockReturnValueOnce(stale.promise).mockReturnValueOnce(current.promise)
    const wrapper = mountDialog()
    const oldSignal = fetchModels.mock.calls[0][0]!
    if (change === 'key change') {
      await wrapper.setProps({ apiKey: { ...key, id: 2, key: 'sk-second-only' } })
    } else {
      await wrapper.setProps({ show: false })
      expect(oldSignal.aborted).toBe(true)
      await wrapper.setProps({ show: true })
    }
    expect(oldSignal.aborted).toBe(true)
    expect(fetchModels.mock.calls[1][0]?.aborted).toBe(false)
    current.resolve(marketplaceFixture())
    await flushPromises()
    stale.resolve([])
    await flushPromises()
    expect(wrapper.find('[role="status"]').exists()).toBe(false)
    await selectModel(wrapper, 'main-model', 'gpt-5.6-sol')
    await submit(wrapper)
    expect(wrapper.emitted('confirm')).toHaveLength(1)
  })

  it('discards a stale rejection while a replacement request remains loading', async () => {
    const stale = deferred<MarketplaceGroup[]>()
    const current = deferred<MarketplaceGroup[]>()
    fetchModels.mockReturnValueOnce(stale.promise).mockReturnValueOnce(current.promise)
    const wrapper = mountDialog()
    await wrapper.setProps({ apiKey: { ...key, id: 2 } })
    stale.reject(new Error('The previous request failed'))
    await flushPromises()
    expect(wrapper.get('[role="status"]').text()).toBe('keys.ccsImport.loadingModels')
    expect(wrapper.find('[data-testid="ccs-retry-models"]').exists()).toBe(false)
    current.resolve(marketplaceFixture())
    await flushPromises()
    expect(wrapper.find('[role="status"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="ccs-confirm"]').attributes('disabled')).toBeUndefined()
  })

  it('aborts an in-flight catalog request when unmounted', async () => {
    const pending = deferred<MarketplaceGroup[]>()
    fetchModels.mockReturnValueOnce(pending.promise)
    const wrapper = mountDialog()
    const signal = fetchModels.mock.calls[0][0]!
    wrapper.unmount()
    wrappers.delete(wrapper)
    expect(signal.aborted).toBe(true)
    pending.resolve(marketplaceFixture())
    await flushPromises()
    expect(wrapper.emitted('confirm')).toBeUndefined()
  })
})
