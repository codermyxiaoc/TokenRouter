import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AdminAccountTestModal from '../AccountTestModal.vue'
import LegacyAccountTestModal from '@/components/account/AccountTestModal.vue'
import { VIDEO_ENDPOINTS } from '@/components/account/videoAccountConfig'
import type { Account } from '@/types'

const { getAvailableModels } = vi.hoisted(() => ({ getAvailableModels: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { accounts: { getAvailableModels } } }))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))

function stream(events: object[]) {
  const bytes = new TextEncoder().encode(events.map(event => `data: ${JSON.stringify(event)}\n\n`).join(''))
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

// 两个账号测试入口共用同一视频契约，禁止在任一入口补开未配置的端点。
describe.each([
  ['admin', AdminAccountTestModal], ['legacy', LegacyAccountTestModal]
] as const)('%s video account tests', (_, component) => {
  beforeEach(() => {
    getAvailableModels.mockReset()
    getAvailableModels.mockResolvedValue([
      { id: 'seed-alias', display_name: 'Seed alias', video_endpoints: ['compat', 'seedance'] },
      { id: 'minimax-model', display_name: 'MiniMax model', video_endpoints: ['minimax'] }
    ])
    vi.stubGlobal('fetch', vi.fn().mockImplementation(async () => stream([{ type: 'test_complete', success: true }])))
  })
  afterEach(() => vi.unstubAllGlobals())

  async function open() {
    const wrapper = mount(component, {
      props: { show: false, account: {
        id: 1, platform: 'video', type: 'apikey', name: 'test', status: 'active',
        credentials: { video_endpoints: [...VIDEO_ENDPOINTS] }
      } as Account },
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

  it('uses model endpoint metadata instead of expanding account-wide capabilities', async () => {
    const wrapper = await open()
    const vm = wrapper.vm as any
    expect(vm.testType).toBe('video')
    expect(wrapper.find('[data-testid="account-test-type"]').exists()).toBe(false)
    expect(vm.videoEndpointOptions.map((option: { value: string }) => option.value)).toEqual(['compat', 'seedance'])
    vm.videoEndpoint = 'seedance'
    vm.selectedModelId = 'minimax-model'
    await flushPromises()
    expect(vm.videoEndpoint).toBe('minimax')
    expect(vm.videoEndpointOptions.map((option: { value: string }) => option.value)).toEqual(['minimax'])
    expect(wrapper.find('[data-testid="account-test-endpoint"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it.each(VIDEO_ENDPOINTS)('submits explicit %s with video parameters and no text transport', async endpoint => {
    getAvailableModels.mockResolvedValue([{ id: 'model-alias', display_name: 'Video', video_endpoints: [endpoint] }])
    const wrapper = await open()
    const vm = wrapper.vm as any
    vm.testPrompt = 'A cat jumps.'
    vm.videoDuration = 8
    vm.videoResolution = ' 768p '
    vm.videoRatio = ' 9:16 '
    vm.uploadImageDataURL = 'data:image/png;base64,AAAA'
    await vm.startTest()
    expect(JSON.parse((fetch as any).mock.calls[0][1].body)).toEqual({
      model_id: 'model-alias', prompt: 'A cat jumps.', test_type: 'video',
      video_endpoint: endpoint, duration: 8, resolution: '768p', ratio: '9:16',
      image_data_url: 'data:image/png;base64,AAAA'
    })
    wrapper.unmount()
  })

  it.each([0, -1, 1.5, 61, '', Number.NaN, Number.POSITIVE_INFINITY])('rejects invalid test duration %s before submission', async duration => {
    const wrapper = await open()
    const vm = wrapper.vm as any
    await wrapper.find('[data-testid="account-test-video-duration"]').setValue(String(duration))
    vm.videoDuration = duration
    await flushPromises()
    expect(vm.canStartTest).toBe(false)
    expect(wrapper.text()).toContain('admin.accounts.video.testDurationInvalid')
    await vm.startTest()
    expect(fetch).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('permits duration boundaries and omits optional blank parameters', async () => {
    const wrapper = await open()
    const vm = wrapper.vm as any
    expect(vm.videoDuration).toBe(5)
    vm.videoResolution = ' '
    vm.videoRatio = ''
    for (const duration of [1, 60]) {
      vm.videoDuration = duration
      await vm.startTest()
      const request = JSON.parse((fetch as any).mock.calls.at(-1)[1].body)
      expect(request.duration).toBe(duration)
      expect(request).not.toHaveProperty('resolution')
      expect(request).not.toHaveProperty('ratio')
    }
    wrapper.unmount()
  })

  it.each([
    { models: [] },
    { models: [{ id: 'missing', display_name: 'Missing metadata' }] },
    { models: [{ id: 'empty', display_name: 'No endpoints', video_endpoints: [] }] },
    { models: [{ id: 'unknown', display_name: 'Unknown endpoint', video_endpoints: ['unknown'] }] }
  ])('blocks missing models or usable endpoints', async ({ models }) => {
    getAvailableModels.mockResolvedValue(models)
    const wrapper = await open()
    const vm = wrapper.vm as any
    expect(vm.videoEndpointOptions).toHaveLength(0)
    expect(vm.canStartTest).toBe(false)
    expect(wrapper.find('[role="alert"]').exists()).toBe(true)
    await vm.startTest()
    expect(fetch).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('renders asynchronous status and the final preview, then resets on account changes', async () => {
    (fetch as any).mockImplementationOnce(async () => stream([
      { type: 'test_start', model: 'seed-alias' },
      { type: 'status', text: 'Waiting for video task' },
      { type: 'video', video_url: 'data:video/mp4;base64,AAAA', mime_type: 'video/mp4' },
      { type: 'test_complete', success: true }
    ]))
    const wrapper = await open()
    const vm = wrapper.vm as any
    vm.videoDuration = 8
    vm.videoResolution = '768p'
    vm.uploadImageDataURL = 'data:image/png;base64,AAAA'
    await vm.startTest()
    await flushPromises()
    expect(vm.status).toBe('success')
    expect(wrapper.text()).toContain('Waiting for video task')
    expect(wrapper.find('video').attributes('src')).toBe('data:video/mp4;base64,AAAA')
    getAvailableModels.mockResolvedValueOnce([{ id: 'other', display_name: 'Other', video_endpoints: ['openai_videos'] }])
    await wrapper.setProps({ account: { id: 2, platform: 'video', type: 'apikey' } as Account })
    await flushPromises()
    expect(vm.videoEndpoint).toBe('openai_videos')
    expect(vm.videoDuration).toBe(5)
    expect(vm.videoResolution).toBe('')
    expect(vm.uploadImageDataURL).toBe('')
    expect(wrapper.find('video').exists()).toBe(false)
    wrapper.unmount()
  })
})
