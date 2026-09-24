import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import AdminAccountTestModal from '../AccountTestModal.vue'
import LegacyAccountTestModal from '@/components/account/AccountTestModal.vue'
import type { Account } from '@/types'

const { getAvailableModels } = vi.hoisted(() => ({ getAvailableModels: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { accounts: { getAvailableModels } } }))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))

function stream(events: object[]) {
  const bytes = new TextEncoder().encode(events.map((event) => `data: ${JSON.stringify(event)}\n\n`).join(''))
  let sent = false
  return {
    ok: true,
    body: { getReader: () => ({ read: async () => {
      if (sent) return { done: true }
      sent = true
      return { done: false, value: bytes }
    } }) }
  }
}

// 同时覆盖在用账号页和兼容入口，防止两份弹窗协议出现偏差。
describe.each([
  ['admin', AdminAccountTestModal], ['legacy', LegacyAccountTestModal]
] as const)('%s account test endpoints', (_, component) => {
  beforeEach(() => {
    getAvailableModels.mockResolvedValue([
      { id: 'grok-4.7', display_name: 'Grok 4.7' },
      { id: 'grok-imagine-image-2.0', display_name: 'Grok Image' },
      { id: 'grok-imagine-video-1.5', display_name: 'Grok Video' }
    ])
    vi.stubGlobal('fetch', vi.fn().mockImplementation(async () => stream([{ type: 'test_complete', success: true }])))
  })
  afterEach(() => vi.unstubAllGlobals())

  async function open(platform: Account['platform'], type: Account['type'] = 'apikey') {
    const wrapper = mount(component, {
      props: { show: false, account: { id: 1, platform, type, name: 'test', status: 'active' } as Account },
      global: { stubs: {
        BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
        Select: { props: ['modelValue', 'options', 'disabled'], template: '<div />' },
        TextArea: { props: ['modelValue'], template: '<textarea />' },
        Icon: true
      } }
    })
    await wrapper.setProps({ show: true })
    await flushPromises()
    return wrapper
  }

  it.each([
    ['openai', 'apikey', ['auto', 'chat_completions', 'responses', 'anthropic']],
    ['openai', 'oauth', ['auto', 'responses']],
    ['anthropic', 'apikey', ['auto', 'chat_completions', 'responses', 'anthropic']],
    ['anthropic', 'oauth', ['auto', 'anthropic']],
    ['anthropic', 'bedrock', ['auto', 'anthropic']],
    ['kimi', 'apikey', ['auto', 'chat_completions', 'responses', 'anthropic']],
    ['deepseek', 'apikey', ['auto', 'chat_completions', 'responses', 'anthropic']],
    ['minimax', 'apikey', ['auto', 'chat_completions', 'responses', 'anthropic']],
    ['zhipu', 'apikey', ['auto', 'chat_completions', 'anthropic']],
    ['gemini', 'service_account', ['auto', 'gemini']],
    ['antigravity', 'oauth', ['auto']],
    ['qoder', 'cosy', ['auto']]
  ])('restricts %s/%s to supported probe transports', async (platform, type, endpoints) => {
    const wrapper = await open(platform as Account['platform'], type as Account['type'])
    expect((wrapper.vm as any).endpointOptions.map((option: { value: string }) => option.value)).toEqual(endpoints)
    wrapper.unmount()
  })

  it.each(['chat_completions', 'responses', 'anthropic'])('sends explicit %s without changing account credentials', async (endpoint) => {
    const wrapper = await open('openai')
    const vm = wrapper.vm as any
    vm.testEndpoint = endpoint
    vm.testPrompt = 'custom prompt'
    await vm.startTest()
    const request = JSON.parse((fetch as any).mock.calls[0][1].body)
    expect(request).toMatchObject({ test_type: 'text', test_endpoint: endpoint, prompt: 'custom prompt' })
    expect(wrapper.props('account')).not.toHaveProperty('credentials.api_protocol')
    wrapper.unmount()
  })

  it('omits automatic endpoint and clears it for compact and image requests', async () => {
    const wrapper = await open('openai')
    const vm = wrapper.vm as any
    await vm.startTest()
    expect(JSON.parse((fetch as any).mock.calls[0][1].body)).not.toHaveProperty('test_endpoint')
    vm.testEndpoint = 'anthropic'
    vm.testMode = 'legacy_compact'
    await flushPromises()
    expect(vm.testEndpoint).toBe('auto')
    await vm.startTest()
    expect(JSON.parse((fetch as any).mock.calls[1][1].body)).toMatchObject({ mode: 'legacy_compact', prompt: '' })
    vm.testType = 'image'
    await flushPromises()
    expect(vm.testMode).toBe('default')
    expect(wrapper.find('[data-testid="account-test-endpoint"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('selects System One for Jev and clears stale endpoint when changing models', async () => {
    getAvailableModels.mockResolvedValue([
      { id: 'jev-1.13', display_name: 'Jev' }, { id: 'gpt-6-sol', display_name: 'GPT' }
    ])
    const wrapper = await open('opencode_go')
    const vm = wrapper.vm as any
    expect(vm.endpointOptions.map((option: { value: string }) => option.value)).toEqual(['auto', 'systemone'])
    vm.testEndpoint = 'systemone'
    await vm.startTest()
    expect(JSON.parse((fetch as any).mock.calls[0][1].body)).toHaveProperty('test_endpoint', 'systemone')
    vm.selectedModelId = 'gpt-6-sol'
    await flushPromises()
    expect(vm.testEndpoint).toBe('auto')
    expect(vm.endpointOptions.map((option: { value: string }) => option.value)).not.toContain('systemone')
    wrapper.unmount()
  })

  it.each(['text', 'image', 'video', 'search', 'tts', 'stt', 'realtime'])('sends Grok %s mode with its own model and media rules', async (mode) => {
    const wrapper = await open('grok', 'oauth')
    const vm = wrapper.vm as any
    expect(vm.testTypeOptions).toHaveLength(7)
    vm.testType = mode
    await flushPromises()
    if (mode === 'image' || mode === 'video') vm.uploadImageDataURL = 'data:image/png;base64,AAAA'
    if (mode === 'stt') vm.uploadAudioDataURL = 'data:audio/wav;base64,AAAA'
    await vm.startTest()
    const request = JSON.parse((fetch as any).mock.calls[0][1].body)
    expect(request.test_type).toBe(mode)
    expect(request).not.toHaveProperty('test_endpoint')
    expect(request).not.toHaveProperty('mode')
    if (mode === 'image') expect(request.model_id).toBe('grok-imagine-image-2.0')
    if (mode === 'video') expect(request.model_id).toBe('grok-imagine-video-1.5')
    if (['search', 'tts', 'stt', 'realtime'].includes(mode)) expect(request.model_id).toBe('')
    if (mode === 'stt') expect(request.audio_data_url).toBe('data:audio/wav;base64,AAAA')
    if (mode === 'image' || mode === 'video') expect(request.image_data_url).toBe('data:image/png;base64,AAAA')
    wrapper.unmount()
  })

  it('renders returned audio and video and clears uploads when switching modes', async () => {
    (fetch as any).mockImplementationOnce(async () => stream([
      { type: 'audio', audio_url: 'data:audio/wav;base64,AAAA', mime_type: 'audio/wav' },
      { type: 'video', video_url: 'https://example.test/result.mp4', mime_type: 'video/mp4' },
      { type: 'test_complete', success: true }
    ]))
    const wrapper = await open('grok')
    const vm = wrapper.vm as any
    vm.testType = 'stt'
    await flushPromises()
    vm.uploadAudioDataURL = 'data:audio/wav;base64,AAAA'
    await vm.startTest()
    await flushPromises()
    expect(wrapper.find('audio').attributes('src')).toBe('data:audio/wav;base64,AAAA')
    expect(wrapper.find('video').attributes('src')).toBe('https://example.test/result.mp4')
    vm.testType = 'text'
    await flushPromises()
    expect(vm.uploadAudioDataURL).toBe('')
    await vm.startTest()
    expect(JSON.parse((fetch as any).mock.calls[1][1].body)).not.toHaveProperty('audio_data_url')
    wrapper.unmount()
  })

  it('discards late model lists after switching accounts', async () => {
    let resolveOld: (models: object[]) => void = () => {}
    getAvailableModels.mockImplementationOnce(() => new Promise((resolve) => { resolveOld = resolve }))
    const wrapper = mount(component, {
      props: { show: false, account: { id: 1, platform: 'openai', type: 'apikey' } as Account },
      global: { stubs: { BaseDialog: true, Select: true, TextArea: true, Icon: true } }
    })
    await wrapper.setProps({ show: true })
    getAvailableModels.mockResolvedValueOnce([{ id: 'new-model', display_name: 'New' }])
    await wrapper.setProps({ account: { id: 2, platform: 'deepseek', type: 'apikey' } as Account })
    await flushPromises()
    resolveOld([{ id: 'old-model', display_name: 'Old' }])
    await flushPromises()
    expect((wrapper.vm as any).selectedModelId).toBe('new-model')
    wrapper.unmount()
  })
  it('reports an interrupted test instead of leaving the loading state active', async () => {
    (fetch as any).mockImplementationOnce(async () => stream([{ type: 'content', text: 'partial' }]))
    const wrapper = await open('grok')
    await (wrapper.vm as any).startTest()
    expect((wrapper.vm as any).status).toBe('error')
    expect((wrapper.vm as any).errorMessage).toBe('admin.accounts.testStreamInterrupted')
    wrapper.unmount()
  })

  it('ignores a response from a closed test after reopening the modal', async () => {
    let finishOld: (response: unknown) => void = () => {}
    (fetch as any).mockImplementationOnce(() => new Promise((resolve) => { finishOld = resolve }))
    const wrapper = await open('grok')
    const vm = wrapper.vm as any
    const pending = vm.startTest()
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await flushPromises()
    await vm.startTest()
    expect(vm.status).toBe('success')
    finishOld({ ok: false, status: 500 })
    await pending
    expect(vm.status).toBe('success')
    wrapper.unmount()
  })

})
