import { flushPromises, mount } from '@vue/test-utils'
import { nextTick } from 'vue'
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
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  async function open(profile?: string) {
    const wrapper = mount(component, {
      props: { show: false, account: {
        id: 1, platform: 'video', type: 'apikey', name: 'test', status: 'active',
        credentials: { video_endpoints: [...VIDEO_ENDPOINTS], ...(profile ? { video_openai_profile: profile } : {}) }
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

  // 两个测试入口仅提交规范化前的测试参数，签名参考图与最终供应商协议由服务端统一处理。
  it('tests Yezonghui with separate resolution, optional ratio and uploaded reference without OpenAI pixel hints', async () => {
    getAvailableModels.mockResolvedValue([{ id: 'relay-model', display_name: 'Relay', video_endpoints: ['openai_videos'], video_protocol_adapters: { openai_videos: 'yezonghui_v1' } }])
    const wrapper = await open('metaso_h3')
    const vm = wrapper.vm as any
    expect(vm.videoProtocolAdapter).toBe('yezonghui_v1')
    expect(vm.videoDuration).toBe(5)
    expect(vm.videoRatio).toBe('')
    expect(wrapper.get('[data-testid="account-test-video-resolution"]').attributes('placeholder')).toBe('720p')
    expect(wrapper.text()).toContain('admin.accounts.video.testYezonghuiResolutionHint')
    expect(wrapper.text()).toContain('admin.accounts.video.testYezonghuiImageHint')
    expect(wrapper.text()).not.toContain('admin.accounts.video.testOpenAIResolutionHint')
    expect(wrapper.text()).not.toContain('admin.accounts.video.testH3ImageHint')
    expect(vm.canStartTest).toBe(false)
    await vm.startTest()
    expect(fetch).not.toHaveBeenCalled()
    vm.videoResolution = '1080p'
    vm.videoDuration = 10
    vm.uploadImageDataURL = 'data:image/png;base64,aGVsbG8='
    await flushPromises()
    expect(vm.canStartTest).toBe(true)
    await vm.startTest()
    expect(fetch).toHaveBeenCalledTimes(1)
    const payload = JSON.parse((fetch as any).mock.calls[0][1].body)
    expect(payload).toMatchObject({ model_id: 'relay-model', test_type: 'video', video_endpoint: 'openai_videos', duration: 10, resolution: '1080p', image_data_url: 'data:image/png;base64,aGVsbG8=' })
    expect(payload).not.toHaveProperty('ratio')
    expect(payload).not.toHaveProperty('video_protocol_adapter')
    expect(payload).not.toHaveProperty('size')
    wrapper.unmount()
  })

  // 同一窗口的并发点击只能创建一个任务，防止管理员测试重复消耗上游额度。
  it('submits only one paid request while repeated start actions race', async () => {
    let resolveRequest!: (response: Response) => void
    vi.mocked(fetch).mockImplementationOnce(() => new Promise(resolve => { resolveRequest = resolve }))
    const wrapper = await open()
    const vm = wrapper.vm as any
    const first = vm.startTest()
    await Promise.all(Array.from({ length: 24 }, () => vm.startTest()))
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(vm.status).toBe('connecting')
    expect(vm.canStartTest).toBe(false)
    resolveRequest(stream([{ type: 'test_complete', success: true }]) as unknown as Response)
    await first
    expect(vm.status).toBe('success')
    wrapper.unmount()
  })

  // 即使网络层迟到返回旧账号结果，切换账号后也不能污染新账号的状态或预览。
  it('ignores a delayed previous account result after changing accounts', async () => {
    let resolveRequest!: (response: Response) => void
    vi.mocked(fetch).mockImplementationOnce(() => new Promise(resolve => { resolveRequest = resolve }))
    const wrapper = await open()
    const vm = wrapper.vm as any
    const pending = vm.startTest()
    const requestSignal = vi.mocked(fetch).mock.calls[0]![1]!.signal!
    await wrapper.setProps({ account: { id: 2, platform: 'video', type: 'apikey' } as Account })
    await flushPromises()
    expect(requestSignal.aborted).toBe(true)
    resolveRequest(stream([
      { type: 'video', video_url: 'https://cdn.example/previous-account.mp4' },
      { type: 'test_complete', success: true }
    ]) as unknown as Response)
    await pending
    await flushPromises()
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(vm.status).toBe('idle')
    expect(vm.generatedVideos).toHaveLength(0)
    expect(wrapper.find('video').exists()).toBe(false)
    wrapper.unmount()
  })

  // 媒体播放失败不改写生成结果，重新加载只能重建播放器，不能发出第二次付费请求。
  it('reports a failed preview separately and reloads the player without generating again', async () => {
    vi.mocked(fetch).mockResolvedValue(stream([
      { type: 'video', video_url: 'https://cdn.example/video.mp4?signature=original' },
      { type: 'test_complete', success: true },
    ]) as unknown as Response)
    const wrapper = await open()
    const vm = wrapper.vm as any
    await vm.startTest()
    await flushPromises()
    expect(vm.status).toBe('success')
    const originalPlayer = wrapper.get('video')
    expect(originalPlayer.attributes('src')).toBe('https://cdn.example/video.mp4?signature=original')
    expect(originalPlayer.attributes('crossorigin')).toBeUndefined()
    await originalPlayer.trigger('error')
    expect(wrapper.get('[data-testid="account-test-video-preview-error"]').text()).toBe('admin.accounts.videoPreviewFailed')
    expect(vm.status).toBe('success')
    const link = wrapper.get('[data-testid="account-test-video-open"]')
    expect(link.attributes()).toMatchObject({ target: '_blank', rel: 'noopener noreferrer', referrerpolicy: 'no-referrer' })
    await wrapper.get('[data-testid="account-test-video-reload"]').trigger('click')
    expect(wrapper.get('video').element).not.toBe(originalPlayer.element)
    expect(wrapper.find('[data-testid="account-test-video-preview-error"]').exists()).toBe(false)
    await originalPlayer.trigger('error')
    expect(wrapper.find('[data-testid="account-test-video-preview-error"]').exists()).toBe(false)
    expect(fetch).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('does not resubmit generation when metadata times out and the preview is reloaded', async () => {
    vi.mocked(fetch).mockResolvedValue(stream([
      { type: 'video', video_url: 'https://cdn.example/slow.mp4' },
      { type: 'test_complete', success: true },
    ]) as unknown as Response)
    const wrapper = await open()
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const vm = wrapper.vm as any
    await vm.startTest()
    await nextTick()
    await vi.advanceTimersByTimeAsync(30_000)
    expect(wrapper.get('[data-testid="account-test-video-preview-error"]').text()).toBe('admin.accounts.videoPreviewTimedOut')
    expect(vm.status).toBe('success')
    await wrapper.get('[data-testid="account-test-video-reload"]').trigger('click')
    expect(wrapper.find('[data-testid="account-test-video-preview-loading"]').exists()).toBe(true)
    expect(fetch).toHaveBeenCalledTimes(1)
    wrapper.unmount()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('plays server-provided video data without creating a data navigation link', async () => {
    vi.mocked(fetch).mockResolvedValue(stream([
      { type: 'video', video_url: 'data:video/mp4;base64,AAAA' },
      { type: 'test_complete', success: true },
    ]) as unknown as Response)
    const wrapper = await open()
    await (wrapper.vm as any).startTest()
    await flushPromises()
    expect(wrapper.get('video').attributes('src')).toBe('data:video/mp4;base64,AAAA')
    expect(wrapper.find('[data-testid="account-test-video-open"]').exists()).toBe(false)
    await wrapper.get('video').trigger('error')
    await wrapper.get('video').trigger('loadedmetadata')
    expect(wrapper.find('[data-testid="account-test-video-preview-error"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it.each(['javascript:alert(1)', 'data:text/html;base64,PHNjcmlwdD4=', 'https://secret:token@cdn.example/video.mp4'])('does not expose unsafe video source as a link: %s', async videoUrl => {
    vi.mocked(fetch).mockResolvedValue(stream([{ type: 'video', video_url: videoUrl }, { type: 'test_complete', success: true }]) as unknown as Response)
    const wrapper = await open()
    await (wrapper.vm as any).startTest()
    await flushPromises()
    expect(wrapper.find('video').exists()).toBe(false)
    expect(wrapper.find('[data-testid="account-test-video-open"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="account-test-video-preview-error"]').exists()).toBe(true)
    wrapper.unmount()
  })

  // 可美名称不能被 compat 的旧透传文案覆盖；相同方案下切换端点也要更新未编辑的默认值。
  it('shows Kemei for compat and applies endpoint-specific test defaults without replacing handwritten values', async () => {
    getAvailableModels.mockResolvedValue([{ id: 'shared', display_name: 'Shared', video_endpoints: ['compat', 'seedance', 'minimax', 'wan', 'kling'], video_protocol_adapters: { compat: 'kemei_v1', seedance: 'kemei_v1', minimax: 'kemei_v1', wan: 'kemei_v1', kling: 'kemei_v1' } }])
    const wrapper = await open()
    const vm = wrapper.vm as any
    expect(wrapper.get('[data-testid="account-test-video-adapter"]').text()).toContain('admin.accounts.video.adapters.kemei_v1')
    expect(vm.videoDuration).toBe(5)
    for (const [endpoint, resolution] of [['seedance', '720p'], ['minimax', '768P'], ['wan', '720P'], ['kling', '720p']]) {
      vm.videoEndpoint = endpoint
      await flushPromises()
      expect(vm.videoResolution).toBe(resolution)
    }
    vm.videoResolution = '1080p'
    vm.videoDuration = 10
    vm.videoEndpoint = 'minimax'
    await flushPromises()
    expect(vm.videoResolution).toBe('1080p')
    expect(vm.videoDuration).toBe(10)
    wrapper.unmount()
  })

  // 两个入口的 Seedance 官方提示不能沿用 768p，也不能把占位文字变成请求默认值。
  it('uses Seedance official resolution guidance while leaving the request default to the upstream', async () => {
    getAvailableModels.mockResolvedValue([{ id: 'seed-official', display_name: 'Seed official', video_endpoints: ['seedance'], video_protocol_adapters: { seedance: 'seedance_v1' } }])
    const wrapper = await open()
    const vm = wrapper.vm as any
    expect(vm.videoResolution).toBe('')
    expect(wrapper.get('[data-testid="account-test-video-resolution"]').attributes('placeholder')).toBe('720p')
    expect(wrapper.text()).toContain('admin.accounts.video.testSeedanceResolutionHint')
    expect(vm.canStartTest).toBe(true)
    await vm.startTest()
    expect(JSON.parse(vi.mocked(fetch).mock.calls.at(-1)![1]!.body as string)).toMatchObject({
      model_id: 'seed-official', video_endpoint: 'seedance', duration: 5,
    })
    expect(JSON.parse(vi.mocked(fetch).mock.calls.at(-1)![1]!.body as string)).not.toHaveProperty('resolution')
    expect(JSON.parse(vi.mocked(fetch).mock.calls.at(-1)![1]!.body as string)).not.toHaveProperty('ratio')
    wrapper.unmount()
  })

  // 不按模型显示名猜测映射结果；官方首帧图和文生请求均继承模型自己的默认比例。
  it.each([
    ['doubao-seedance-2-5', false], ['doubao-seedance-2-5', true],
    ['doubao-seedance-2-5-261014', true], ['public-model-alias', true],
    ['doubao-seedance-1-0-pro', true], ['doubao-seedance-2-0-mini', false],
  ] as const)('omits an unedited official Seedance ratio for %s with image=%s', async (model, image) => {
    getAvailableModels.mockResolvedValue([{ id: model, display_name: model, video_endpoints: ['seedance'], video_protocol_adapters: { seedance: 'seedance_v1' } }])
    const wrapper = await open()
    const vm = wrapper.vm as any
    expect(vm.videoRatio).toBe('')
    expect(wrapper.get('[data-testid="account-test-video-ratio"]').attributes('placeholder')).toBe('admin.accounts.video.testSeedanceRatioPlaceholder')
    const referenceImage = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl6Y20AAAAASUVORK5CYII='
    if (image) {
      // 使用实际上传入口，确保读取图片后不会重新注入通用的 16:9 默认值。
      const bytes = Uint8Array.from(atob(referenceImage.split(',')[1]!), char => char.charCodeAt(0))
      await vm.onImageFileChange({ target: { files: [new File([bytes], 'first-frame.png', { type: 'image/png' })], value: 'selected' } })
    }
    await flushPromises()
    expect(vm.canStartTest).toBe(true)
    await vm.startTest()
    const request = JSON.parse(vi.mocked(fetch).mock.calls.at(-1)![1]!.body as string)
    expect(request).toMatchObject({ model_id: model, video_endpoint: 'seedance', duration: 5 })
    expect(request).not.toHaveProperty('ratio')
    expect(request).not.toHaveProperty('resolution')
    if (image) expect(request.image_data_url).toBe(referenceImage)
    else expect(request).not.toHaveProperty('image_data_url')
    expect(fetch).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  // 旧协议和可美仍使用原默认比例；只有切到官方方案时清空未编辑的默认值。
  it.each(['legacy_v1', 'kemei_v1'] as const)('isolates Seedance official ratio defaults from %s', async adapter => {
    getAvailableModels.mockResolvedValue([
      { id: 'seed-existing', display_name: 'Existing', video_endpoints: ['seedance'], video_protocol_adapters: { seedance: adapter } },
      { id: 'seed-official', display_name: 'Official', video_endpoints: ['seedance'], video_protocol_adapters: { seedance: 'seedance_v1' } },
    ])
    const wrapper = await open()
    const vm = wrapper.vm as any
    vm.uploadImageDataURL = 'data:image/png;base64,AAAA'
    expect(vm.videoRatio).toBe('16:9')
    expect(wrapper.get('[data-testid="account-test-video-ratio"]').attributes('placeholder')).toBe('16:9')
    await vm.startTest()
    expect(JSON.parse(vi.mocked(fetch).mock.calls.at(-1)![1]!.body as string).ratio).toBe('16:9')
    vm.selectedModelId = 'seed-official'
    await flushPromises()
    expect(vm.videoRatio).toBe('')
    await vm.startTest()
    expect(JSON.parse(vi.mocked(fetch).mock.calls.at(-1)![1]!.body as string)).not.toHaveProperty('ratio')
    vm.selectedModelId = 'seed-existing'
    await flushPromises()
    expect(vm.videoRatio).toBe('16:9')
    await vm.startTest()
    expect(JSON.parse(vi.mocked(fetch).mock.calls.at(-1)![1]!.body as string).ratio).toBe('16:9')
    wrapper.unmount()
  })

  // 手工输入值（包括原默认值和清空）不能被方案切换覆盖；最终模型规则由后端校验。
  it.each(['16:9', ' adaptive ', ''])('preserves explicit Seedance ratio %j across protocol changes', async ratio => {
    getAvailableModels.mockResolvedValue([
      { id: 'seed-existing', display_name: 'Existing', video_endpoints: ['seedance'], video_protocol_adapters: { seedance: 'legacy_v1' } },
      { id: 'mapped-alias', display_name: 'Official alias', video_endpoints: ['seedance'], video_protocol_adapters: { seedance: 'seedance_v1' } },
    ])
    const wrapper = await open()
    const vm = wrapper.vm as any
    await wrapper.get('[data-testid="account-test-video-ratio"]').setValue(ratio)
    for (const model of ['mapped-alias', 'seed-existing', 'mapped-alias']) {
      vm.selectedModelId = model
      await flushPromises()
      expect(vm.videoRatio).toBe(ratio)
    }
    vm.uploadImageDataURL = 'data:image/png;base64,AAAA'
    await vm.startTest()
    const request = JSON.parse(vi.mocked(fetch).mock.calls.at(-1)![1]!.body as string)
    if (ratio.trim()) expect(request.ratio).toBe(ratio.trim())
    else expect(request).not.toHaveProperty('ratio')
    expect(fetch).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('shows a mapped-model ratio rejection without retrying or silently changing the input', async () => {
    getAvailableModels.mockResolvedValue([{ id: 'mapped-alias', display_name: 'Alias', video_endpoints: ['seedance'], video_protocol_adapters: { seedance: 'seedance_v1' } }])
    vi.mocked(fetch).mockResolvedValue(stream([{ type: 'error', error: 'Seedance 2.5 首帧/尾帧模式仅支持 adaptive 比例' }]) as unknown as Response)
    const wrapper = await open()
    const vm = wrapper.vm as any
    await wrapper.get('[data-testid="account-test-video-ratio"]').setValue('16:9')
    vm.uploadImageDataURL = 'data:image/png;base64,AAAA'
    await vm.startTest()
    await flushPromises()
    expect(vm.status).toBe('error')
    expect(wrapper.text()).toContain('Seedance 2.5 首帧/尾帧模式仅支持 adaptive 比例')
    expect(vm.videoRatio).toBe('16:9')
    expect(fetch).toHaveBeenCalledTimes(1)
    expect(wrapper.find('video').exists()).toBe(false)
    wrapper.unmount()
  })

  // 切换账号必须重置编辑标记，不能把上一账号的手填比例带入新账号的官方测试。
  it('resets a handwritten ratio when switching to another official Seedance account', async () => {
    getAvailableModels.mockResolvedValue([{ id: 'seed-official', display_name: 'Official', video_endpoints: ['seedance'], video_protocol_adapters: { seedance: 'seedance_v1' } }])
    const wrapper = await open()
    const vm = wrapper.vm as any
    await wrapper.get('[data-testid="account-test-video-ratio"]').setValue('16:9')
    await wrapper.setProps({ account: { id: 2, platform: 'video', type: 'apikey' } as Account })
    await flushPromises()
    expect(vm.videoRatio).toBe('')
    await vm.startTest()
    expect(JSON.parse(vi.mocked(fetch).mock.calls.at(-1)![1]!.body as string)).not.toHaveProperty('ratio')
    expect(fetch).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('preserves an explicitly confirmed empty official ratio when selecting a legacy model', async () => {
    getAvailableModels.mockResolvedValue([
      { id: 'seed-official', display_name: 'Official', video_endpoints: ['seedance'], video_protocol_adapters: { seedance: 'seedance_v1' } },
      { id: 'seed-existing', display_name: 'Existing', video_endpoints: ['seedance'], video_protocol_adapters: { seedance: 'legacy_v1' } },
    ])
    const wrapper = await open()
    const vm = wrapper.vm as any
    expect(vm.videoRatio).toBe('')
    // 值未变化的输入事件仍算用户确认，切换协议时不能补回 16:9。
    await wrapper.get('[data-testid="account-test-video-ratio"]').setValue('')
    vm.selectedModelId = 'seed-existing'
    await flushPromises()
    expect(vm.videoRatio).toBe('')
    await vm.startTest()
    expect(JSON.parse(vi.mocked(fetch).mock.calls.at(-1)![1]!.body as string)).not.toHaveProperty('ratio')
    wrapper.unmount()
  })

  // 分辨率可大小写混写；浏览器不能根据公开别名猜测最终模型的档位限制。
  it.each(['480p', '720P', '1080p', ' 4K '])('accepts official Seedance resolution %s without rewriting the input', async resolution => {
    getAvailableModels.mockResolvedValue([{ id: 'public-model-alias', display_name: 'Alias', video_endpoints: ['seedance'], video_protocol_adapters: { seedance: 'seedance_v1' } }])
    const wrapper = await open()
    const vm = wrapper.vm as any
    await wrapper.get('[data-testid="account-test-video-resolution"]').setValue(resolution)
    expect(vm.videoResolution).toBe(resolution)
    expect(vm.canStartTest).toBe(true)
    await vm.startTest()
    expect(JSON.parse(vi.mocked(fetch).mock.calls.at(-1)![1]!.body as string).resolution).toBe(resolution.trim())
    wrapper.unmount()
  })

  // 非官方档位在付费创建前拦截，保留管理员输入供修正，不自动替换为另一种分辨率。
  it.each(['768p', '2K', '1280x720'])('rejects official Seedance resolution %s before sending a request', async resolution => {
    getAvailableModels.mockResolvedValue([{ id: 'seed-official', display_name: 'Seed official', video_endpoints: ['seedance'], video_protocol_adapters: { seedance: 'seedance_v1' } }])
    const wrapper = await open()
    const vm = wrapper.vm as any
    await wrapper.get('[data-testid="account-test-video-resolution"]').setValue(resolution)
    expect(vm.canStartTest).toBe(false)
    expect(wrapper.text()).toContain('admin.accounts.video.testSeedanceResolutionInvalid')
    await vm.startTest()
    expect(fetch).not.toHaveBeenCalled()
    expect(vm.videoResolution).toBe(resolution)
    wrapper.unmount()
  })

  // 只约束官方方案，切换模型时不能覆盖手填值，也不能把官方限制套用到已有协议或可美。
  it.each(['legacy_v1', 'kemei_v1'] as const)('preserves handwritten resolution and existing %s behavior across Seedance model changes', async adapter => {
    getAvailableModels.mockResolvedValue([
      { id: 'seed-existing', display_name: 'Existing', video_endpoints: ['seedance'], video_protocol_adapters: { seedance: adapter } },
      { id: 'seed-official', display_name: 'Official', video_endpoints: ['seedance'], video_protocol_adapters: { seedance: 'seedance_v1' } },
    ])
    const wrapper = await open()
    const vm = wrapper.vm as any
    expect(vm.videoResolution).toBe(adapter === 'kemei_v1' ? '720p' : '')
    await wrapper.get('[data-testid="account-test-video-resolution"]').setValue('768p')
    expect(vm.canStartTest).toBe(true)
    vm.selectedModelId = 'seed-official'
    await flushPromises()
    expect(vm.videoResolution).toBe('768p')
    expect(vm.canStartTest).toBe(false)
    vm.selectedModelId = 'seed-existing'
    await flushPromises()
    expect(vm.videoResolution).toBe('768p')
    expect(vm.canStartTest).toBe(true)
    await vm.startTest()
    expect(JSON.parse(vi.mocked(fetch).mock.calls.at(-1)![1]!.body as string)).toMatchObject({
      model_id: 'seed-existing', video_endpoint: 'seedance', resolution: '768p',
    })
    wrapper.unmount()
  })

  // 参数格式切换不能改变兼容入口路径，也不能在浏览器伪造服务端生效方案。
  it('uses OpenAI test fields on compat while preserving the selected compatibility endpoint', async () => {
    getAvailableModels.mockResolvedValue([{ id: 'shared', display_name: 'Shared', video_endpoints: ['compat'], video_protocol_adapters: { compat: 'openai_v1' } }])
    const wrapper = await open()
    const vm = wrapper.vm as any
    expect(vm.videoProtocolAdapter).toBe('openai_v1')
    expect(wrapper.get('[data-testid="account-test-video-adapter"]').text()).toContain('admin.accounts.video.adapters.compat_openai')
    expect(vm.videoDuration).toBe(4)
    expect(vm.videoResolution).toBe('')
    expect(vm.videoRatio).toBe('')
    expect(wrapper.get('[data-testid="account-test-video-resolution"]').attributes('placeholder')).toBe('1280x720')
    await wrapper.get('[data-testid="account-test-video-resolution"]').setValue('1280x720')
    await vm.startTest()
    const body = JSON.parse((fetch as any).mock.calls.at(-1)[1].body)
    expect(body).toMatchObject({ model_id: 'shared', video_endpoint: 'compat', duration: 4, resolution: '1280x720' })
    expect(body).not.toHaveProperty('ratio')
    expect(body).not.toHaveProperty('video_protocol_adapter')
    wrapper.unmount()
  })

  it('labels the legacy compatibility scheme consistently without changing its test defaults', async () => {
    const wrapper = await open()
    const vm = wrapper.vm as any
    expect(vm.videoEndpoint).toBe('compat')
    expect(wrapper.get('[data-testid="account-test-video-adapter"]').text()).toContain('admin.accounts.video.adapters.legacy_v1')
    expect(vm.videoDuration).toBe(5)
    expect(vm.videoRatio).toBe('16:9')
    wrapper.unmount()
  })

  it('does not inject Kemei resolution or ratio into an official Vidu account test', async () => {
    getAvailableModels.mockResolvedValue([{ id: 'vidu-official', display_name: 'Vidu official', video_endpoints: ['vidu'], video_protocol_adapters: { vidu: 'vidu_v2' }, video_vidu_operation: '/ent/v2/text2video' }])
    const wrapper = await open()
    const vm = wrapper.vm as any
    expect(vm.videoProtocolAdapter).toBe('vidu_v2')
    expect(vm.videoDuration).toBe(5)
    expect(vm.videoResolution).toBe('')
    expect(vm.videoRatio).toBe('')
    expect(wrapper.text()).toContain('admin.accounts.video.testViduResolutionHint')
    expect(wrapper.get('[data-testid="account-test-video-resolution"]').attributes('placeholder')).toBe('admin.accounts.video.testViduResolutionPlaceholder')
    expect(vm.canStartTest).toBe(true)
    await vm.startTest()
    const body = JSON.parse((fetch as any).mock.calls.at(-1)[1].body)
    expect(body).toMatchObject({ video_endpoint: 'vidu', video_operation: '/ent/v2/text2video', duration: 5 })
    expect(body).not.toHaveProperty('resolution')
    expect(body).not.toHaveProperty('ratio')
    expect(body).not.toHaveProperty('video_protocol_adapter')
    wrapper.unmount()
  })

  it('omits only the unedited MiniMax Kemei image ratio and keeps text-to-video or explicit ratios', async () => {
    getAvailableModels.mockResolvedValue([{ id: 'h3', display_name: 'H3', video_endpoints: ['minimax'], video_protocol_adapters: { minimax: 'kemei_v1' } }])
    const wrapper = await open()
    const vm = wrapper.vm as any
    await vm.startTest()
    expect(JSON.parse((fetch as any).mock.calls.at(-1)[1].body)).toMatchObject({ ratio: '16:9', resolution: '768P' })
    vm.uploadImageDataURL = 'data:image/png;base64,AAAA'
    await vm.startTest()
    const withImage = JSON.parse((fetch as any).mock.calls.at(-1)[1].body)
    expect(withImage).not.toHaveProperty('ratio')
    expect(withImage.image_data_url).toBe('data:image/png;base64,AAAA')
    await wrapper.get('[data-testid="account-test-video-ratio"]').setValue('16:9')
    await vm.startTest()
    expect(JSON.parse((fetch as any).mock.calls.at(-1)[1].body).ratio).toBe('16:9')
    wrapper.unmount()
  })

  // Vidu 操作必须明确，测试的图片数量限制在 POST 前校验，不能消耗上游额度后才发现缺图。
  it('requires an explicit Vidu operation and validates reference images before testing', async () => {
    getAvailableModels.mockResolvedValue([{ id: 'vidu-model', display_name: 'Vidu', video_endpoints: ['vidu'], video_protocol_adapters: { vidu: 'kemei_v1' } }])
    const wrapper = await open()
    const vm = wrapper.vm as any
    expect(vm.videoDuration).toBe(5)
    expect(vm.videoResolution).toBe('540p')
    expect(vm.videoRatio).toBe('16:9')
    expect(vm.canStartTest).toBe(false)
    expect(wrapper.text()).toContain('admin.accounts.video.testViduOperationRequired')
    vm.videoOperation = '/ent/v2/img2video'
    await flushPromises()
    expect(vm.canStartTest).toBe(false)
    expect(wrapper.text()).toContain('admin.accounts.video.testViduImageRequired')
    await vm.startTest()
    expect(fetch).not.toHaveBeenCalled()
    vm.uploadImageDataURL = 'data:image/png;base64,AAAA'
    await flushPromises()
    expect(vm.canStartTest).toBe(true)
    await vm.startTest()
    expect(JSON.parse((fetch as any).mock.calls.at(-1)[1].body)).toMatchObject({ video_endpoint: 'vidu', video_operation: '/ent/v2/img2video', duration: 5, resolution: '540p', image_data_url: 'data:image/png;base64,AAAA' })
    vm.videoOperation = '/ent/v2/text2video'
    await flushPromises()
    expect(vm.canStartTest).toBe(false)
    vm.clearMediaUploads()
    await flushPromises()
    expect(vm.canStartTest).toBe(true)
    await vm.startTest()
    expect(JSON.parse((fetch as any).mock.calls.at(-1)[1].body)).not.toHaveProperty('image_data_url')
    vm.videoOperation = '/ent/v2/start-end2video'
    await flushPromises()
    expect(vm.canStartTest).toBe(false)
    expect(wrapper.text()).toContain('admin.accounts.video.testViduTwoImagesRequired')
    vm.videoOperation = 'https://other.test/ent/v2/img2video'
    await flushPromises()
    expect(vm.canStartTest).toBe(false)
    expect(fetch).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })

  it('restores the model-specific Vidu operation and clears it when switching to an unconfigured model', async () => {
    getAvailableModels.mockResolvedValue([
      { id: 'configured', display_name: 'Configured', video_endpoints: ['vidu'], video_protocol_adapters: { vidu: 'kemei_v1' }, video_vidu_operation: '/ent/v2/reference2video' },
      { id: 'unconfigured', display_name: 'Unconfigured', video_endpoints: ['vidu'], video_protocol_adapters: { vidu: 'kemei_v1' } },
    ])
    const wrapper = await open()
    const vm = wrapper.vm as any
    expect(vm.videoOperation).toBe('/ent/v2/reference2video')
    vm.selectedModelId = 'unconfigured'
    await flushPromises()
    expect(vm.videoOperation).toBe('')
    expect(vm.canStartTest).toBe(false)
    wrapper.unmount()
  })

  // 服务器返回的有效适配器包含最终模型覆盖，应优先于账号旧 H3 字段。
  it('uses mapped model adapter metadata and keeps handwritten inputs when switching endpoints', async () => {
    getAvailableModels.mockResolvedValue([
      { id: 'public-alias', display_name: 'Alias', video_endpoints: ['openai_videos', 'minimax'], video_protocol_adapters: { openai_videos: 'openai_v1', minimax: 'minimax_v2' } },
      { id: 'h3-alias', display_name: 'H3', video_endpoints: ['openai_videos'], video_protocol_adapters: { openai_videos: 'metaso_h3_v1' } },
    ])
    const wrapper = await open('metaso_h3')
    const vm = wrapper.vm as any
    expect(vm.videoProtocolAdapter).toBe('openai_v1')
    expect(vm.videoDuration).toBe(4)
    expect(vm.videoRatio).toBe('')
    expect(wrapper.get('[data-testid="account-test-video-resolution"]').attributes('placeholder')).toBe('1280x720')
    expect(wrapper.text()).toContain('admin.accounts.video.testOpenAIResolutionHint')
    vm.videoEndpoint = 'minimax'
    await flushPromises()
    expect(vm.videoDuration).toBe(5)
    expect(vm.videoResolution).toBe('768P')
    vm.videoDuration = 8
    vm.videoResolution = '2K'
    vm.videoRatio = '9:16'
    vm.selectedModelId = 'h3-alias'
    await flushPromises()
    expect(vm.videoProtocolAdapter).toBe('metaso_h3_v1')
    expect(vm.videoDuration).toBe(8)
    expect(vm.videoResolution).toBe('2K')
    expect(vm.videoRatio).toBe('9:16')
    expect(vm.canStartTest).toBe(true)
    await vm.startTest()
    expect(JSON.parse((fetch as any).mock.calls.at(-1)[1].body)).toMatchObject({
      model_id: 'h3-alias', video_endpoint: 'openai_videos', duration: 8, resolution: '2K', ratio: '9:16',
    })
    // 客户端不能用测试载荷临时改写账号中的协议方案，最终选择由服务端决定。
    expect(JSON.parse((fetch as any).mock.calls.at(-1)[1].body)).not.toHaveProperty('video_protocol_adapter')
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(vm.selectedModelId).toBe('public-alias')
    expect(vm.videoDuration).toBe(4)
    expect(vm.videoResolution).toBe('')
    wrapper.unmount()
  })

  it('does not apply H3 validation when the model explicitly overrides its endpoint with legacy protocol', async () => {
    getAvailableModels.mockResolvedValue([{ id: 'h3-alias', display_name: 'H3', video_endpoints: ['openai_videos'], video_protocol_adapters: { openai_videos: 'legacy_v1' } }])
    const wrapper = await open('metaso_h3')
    const vm = wrapper.vm as any
    expect(vm.videoProtocolAdapter).toBe('legacy_v1')
    expect(vm.canStartTest).toBe(true)
    expect(wrapper.text()).not.toContain('admin.accounts.video.testH3ImageHint')
    expect(vm.videoDuration).toBe(5)
    wrapper.unmount()
  })

  it('preserves an explicitly entered default duration instead of resetting it on an adapter switch', async () => {
    getAvailableModels.mockResolvedValue([{ id: 'video', display_name: 'Video', video_endpoints: ['minimax', 'openai_videos'], video_protocol_adapters: { minimax: 'minimax_v2', openai_videos: 'openai_v1' } }])
    const wrapper = await open()
    const vm = wrapper.vm as any
    vm.videoEndpoint = 'minimax'
    await flushPromises()
    expect(vm.videoDuration).toBe(5)
    await wrapper.get('[data-testid="account-test-video-duration"]').setValue('5')
    vm.videoEndpoint = 'openai_videos'
    await flushPromises()
    expect(vm.videoDuration).toBe(5)
    wrapper.unmount()
  })

  // 两个入口都在发送前提示 H3 限制，但不把这些限制施加给透传或其他原生端点。
  it('validates explicit H3 geometry and preserves the five-second test default', async () => {
    getAvailableModels.mockResolvedValue([{ id: 'h3', display_name: 'H3', video_endpoints: ['openai_videos', 'compat'] }])
    const wrapper = await open('metaso_h3')
    const vm = wrapper.vm as any
    vm.videoEndpoint = 'openai_videos'
    await flushPromises()
    expect(vm.canStartTest).toBe(false)
    expect(wrapper.text()).toContain('admin.accounts.video.testH3ResolutionInvalid')
    expect(wrapper.text()).toContain('admin.accounts.video.testH3ImageHint')
    expect(wrapper.find('input[type="file"][accept*="image"]').attributes('multiple')).toBeUndefined()
    vm.videoResolution = '768p'
    vm.videoRatio = '1:1'
    await flushPromises()
    expect(wrapper.text()).toContain('admin.accounts.video.testH3RatioInvalid')
    await vm.startTest()
    expect(fetch).not.toHaveBeenCalled()
    for (const [resolution, ratio] of [['768p', '16:9'], ['2K', '9:16']]) {
      vm.videoResolution = resolution
      vm.videoRatio = ratio
      await flushPromises()
      expect(vm.canStartTest).toBe(true)
      await vm.startTest()
      expect(JSON.parse((fetch as any).mock.calls.at(-1)[1].body)).toMatchObject({ duration: 5, resolution, ratio, video_endpoint: 'openai_videos' })
    }
    vm.videoEndpoint = 'compat'
    vm.videoResolution = '540p'
    vm.videoRatio = '1:1'
    await flushPromises()
    expect(vm.canStartTest).toBe(true)
    expect(wrapper.text()).not.toContain('admin.accounts.video.testH3ImageHint')
    await vm.startTest()
    expect(JSON.parse((fetch as any).mock.calls.at(-1)[1].body)).toMatchObject({ resolution: '540p', ratio: '1:1', video_endpoint: 'compat' })
    wrapper.unmount()
  })

  it('does not infer H3 restrictions from the model name for passthrough accounts', async () => {
    getAvailableModels.mockResolvedValue([{ id: 'MiniMax-H3', display_name: 'H3', video_endpoints: ['openai_videos'] }])
    const wrapper = await open('passthrough')
    const vm = wrapper.vm as any
    vm.videoResolution = '540p'
    vm.videoRatio = '1:1'
    await flushPromises()
    expect(vm.canStartTest).toBe(true)
    await vm.startTest()
    expect(JSON.parse((fetch as any).mock.calls.at(-1)[1].body)).toMatchObject({ resolution: '540p', ratio: '1:1', duration: 5 })
    wrapper.unmount()
  })

  // 无图与有图都允许生成，旧账号及显式适配账号共用两个管理入口，图片始终为可选项。
  it.each([
    { profile: undefined, image: false },
    { profile: undefined, image: true },
    { profile: 'passthrough', image: false },
    { profile: 'passthrough', image: true },
    { profile: 'metaso_h3', image: false },
    { profile: 'metaso_h3', image: true },
  ])('submits optional reference image=$image with profile=$profile', async ({ profile, image }) => {
    getAvailableModels.mockResolvedValue([{ id: 'MiniMax-H3', display_name: 'H3', video_endpoints: ['openai_videos'] }])
    const wrapper = await open(profile)
    const vm = wrapper.vm as any
    vm.testPrompt = 'A cat walks through a garden.'
    vm.videoDuration = 5
    vm.videoResolution = '768p'
    vm.videoRatio = '16:9'
    const referenceImage = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Wl6Y20AAAAASUVORK5CYII='
    if (image) {
      // 沿用实际选图与 FileReader 流程，校验上传结果进入请求而非直接设置内部状态。
      const bytes = Uint8Array.from(atob(referenceImage.split(',')[1]), char => char.charCodeAt(0))
      await vm.onImageFileChange({ target: { files: [new File([bytes], 'reference.png', { type: 'image/png' })], value: 'selected' } })
    }
    await flushPromises()
    expect(vm.canStartTest).toBe(true)
    expect(wrapper.find('input[type="file"]').attributes('required')).toBeUndefined()
    await vm.startTest()
    expect(fetch).toHaveBeenCalledTimes(1)
    const request = JSON.parse((fetch as any).mock.calls[0][1].body)
    expect(request).toEqual({
      model_id: 'MiniMax-H3', prompt: 'A cat walks through a garden.', test_type: 'video',
      video_endpoint: 'openai_videos', duration: 5, resolution: '768p', ratio: '16:9',
      ...(image ? { image_data_url: referenceImage } : {}),
    })
    expect(vm.status).toBe('success')
    if (image) {
      // 去掉参考图后再测，上一轮图像不能被夹带到纯文本生成请求中。
      vm.clearMediaUploads()
      await flushPromises()
      expect(vm.canStartTest).toBe(true)
      await vm.startTest()
      expect(fetch).toHaveBeenCalledTimes(2)
      expect(JSON.parse((fetch as any).mock.calls[1][1].body)).not.toHaveProperty('image_data_url')
    }
    wrapper.unmount()
  })

  // 账号编辑后重开测试必须采用新配置，移除标准端点不能留下可发起的旧选项。
  it('refreshes profile validation and endpoint visibility when the same account configuration changes', async () => {
    getAvailableModels.mockResolvedValue([{ id: 'h3', display_name: 'H3', video_endpoints: ['openai_videos'] }])
    const wrapper = await open('passthrough')
    const vm = wrapper.vm as any
    expect(vm.canStartTest).toBe(true)
    await wrapper.setProps({ account: {
      ...wrapper.props('account')!, credentials: { video_endpoints: ['openai_videos'], video_openai_profile: 'metaso_h3' }
    } as Account })
    await flushPromises()
    expect(vm.canStartTest).toBe(false)
    expect(wrapper.text()).toContain('admin.accounts.video.testH3ResolutionInvalid')
    expect(wrapper.find('label[for="account-test-video-resolution"]').text()).toBe('admin.accounts.video.testH3Resolution')
    getAvailableModels.mockResolvedValue([{ id: 'h3', display_name: 'H3', video_endpoints: ['compat'] }])
    await wrapper.setProps({ account: {
      ...wrapper.props('account')!, credentials: { video_endpoints: ['compat'], video_openai_profile: 'metaso_h3' }
    } as Account })
    await flushPromises()
    expect(vm.videoEndpoint).toBe('compat')
    expect(vm.videoEndpointOptions.map((option: { value: string }) => option.value)).toEqual(['compat'])
    expect(vm.canStartTest).toBe(true)
    expect(wrapper.text()).not.toContain('admin.accounts.video.testH3ImageHint')
    expect(wrapper.find('label[for="account-test-video-resolution"]').text()).toBe('admin.accounts.video.testResolution')
    await vm.startTest()
    expect(JSON.parse((fetch as any).mock.calls.at(-1)[1].body).video_endpoint).toBe('compat')
    getAvailableModels.mockResolvedValue([{ id: 'h3', display_name: 'H3', video_endpoints: ['openai_videos'] }])
    await wrapper.setProps({ account: {
      ...wrapper.props('account')!, credentials: { video_endpoints: ['openai_videos'], video_openai_profile: 'passthrough' }
    } as Account })
    await flushPromises()
    expect(vm.videoEndpoint).toBe('openai_videos')
    expect(vm.canStartTest).toBe(true)
    expect(wrapper.text()).not.toContain('admin.accounts.video.testH3ResolutionInvalid')
    wrapper.unmount()
  })

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

  it.each(VIDEO_ENDPOINTS.filter(endpoint => endpoint !== 'vidu'))('submits explicit %s with video parameters and no text transport', async endpoint => {
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
