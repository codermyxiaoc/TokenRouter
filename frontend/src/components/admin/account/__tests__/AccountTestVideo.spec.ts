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

  // MiniMax 的占位提示不替代输入值，大小写属于上游合同，两个管理入口都必须保真。
  it.each(['2K', '768P'])('preserves MiniMax resolution %s exactly', async resolution => {
    getAvailableModels.mockResolvedValue([{ id: 'MiniMax-H3', display_name: 'H3', video_endpoints: ['minimax'] }])
    const wrapper = await open()
    const vm = wrapper.vm as any
    const input = wrapper.find('[data-testid="account-test-video-resolution"]')
    expect(input.attributes('placeholder')).toBe('2K')
    expect(vm.videoResolution).toBe('')
    await input.setValue(` ${resolution} `)
    await vm.startTest()
    expect(JSON.parse((fetch as any).mock.calls[0][1].body)).toMatchObject({
      model_id: 'MiniMax-H3', video_endpoint: 'minimax', duration: 5, resolution,
    })
    wrapper.unmount()
  })

  it('does not submit the MiniMax resolution placeholder and restores the other endpoint hint', async () => {
    getAvailableModels.mockResolvedValue([{ id: 'MiniMax-H3', display_name: 'H3', video_endpoints: ['minimax', 'compat'] }])
    const wrapper = await open()
    const vm = wrapper.vm as any
    vm.videoEndpoint = 'minimax'
    await flushPromises()
    const input = wrapper.find('[data-testid="account-test-video-resolution"]')
    expect(input.attributes('placeholder')).toBe('2K')
    await input.setValue(' ')
    await vm.startTest()
    const request = JSON.parse((fetch as any).mock.calls[0][1].body)
    expect(request).toMatchObject({ video_endpoint: 'minimax', duration: 5 })
    expect(request).not.toHaveProperty('resolution')
    vm.videoEndpoint = 'compat'
    await flushPromises()
    expect(input.attributes('placeholder')).toBe('768p')
    expect(vm.videoResolution).toBe(' ')
    wrapper.unmount()
  })

  // 公网 URL 只随测试 POST 传给后端，不在浏览器抓图；水印未设置时不替供应商猜默认值。
  it.each(['unset', 'on', 'off'])('submits a MiniMax reference URL with watermark %s', async watermark => {
    getAvailableModels.mockResolvedValue([{ id: 'MiniMax-H3', display_name: 'H3', video_endpoints: ['minimax'] }])
    const wrapper = await open()
    const vm = wrapper.vm as any
    expect(vm.videoAigcWatermark).toBe('unset')
    expect(vm.videoWatermarkOptions.map((option: { value: string }) => option.value)).toEqual(['unset', 'on', 'off'])
    expect(wrapper.find('[data-testid="account-test-watermark"]').exists()).toBe(true)
    await wrapper.find('[data-testid="account-test-reference-image-url"]').setValue(' https://media.example.com/first-frame.png?signature=test ')
    vm.videoAigcWatermark = watermark
    await vm.startTest()
    expect(fetch).toHaveBeenCalledTimes(1)
    const request = JSON.parse((fetch as any).mock.calls[0][1].body)
    expect(request).toMatchObject({ video_endpoint: 'minimax', reference_image_url: 'https://media.example.com/first-frame.png?signature=test' })
    expect(request).not.toHaveProperty('image_data_url')
    expect(request).not.toHaveProperty('reference_image_transport')
    if (watermark === 'unset') expect(request).not.toHaveProperty('aigc_watermark')
    else expect(request.aigc_watermark).toBe(watermark === 'on')
    expect(wrapper.find('img[src^="https://media.example.com"]').exists()).toBe(false)
    wrapper.unmount()
  })

  // 转存是上传图片的显式选择，不能让默认测试、纯文本生成或手填 URL 触发存储写入。
  it.each([
    { transport: 'data_url', image: false }, { transport: 'data_url', image: true },
    { transport: 'https_url', image: false }, { transport: 'https_url', image: true },
  ])('submits uploaded reference transport=$transport only when image=$image requires it', async ({ transport, image }) => {
    getAvailableModels.mockResolvedValue([{ id: 'MiniMax-H3-open', display_name: 'H3-open', video_endpoints: ['minimax'] }])
    const wrapper = await open()
    const vm = wrapper.vm as any
    expect(vm.videoReferenceImageTransport).toBe('data_url')
    expect(vm.videoReferenceImageTransportOptions.map((option: { value: string }) => option.value)).toEqual(['data_url', 'https_url'])
    expect(wrapper.find('[data-testid="account-test-reference-image-transport"]').exists()).toBe(true)
    vm.videoReferenceImageTransport = transport
    if (image) vm.uploadImageDataURL = 'data:image/png;base64,AAAA'
    await vm.startTest()
    const request = JSON.parse((fetch as any).mock.calls[0][1].body)
    if (image && transport === 'https_url') expect(request.reference_image_transport).toBe('https_url')
    else expect(request).not.toHaveProperty('reference_image_transport')
    if (image) expect(request.image_data_url).toBe('data:image/png;base64,AAAA')
    else expect(request).not.toHaveProperty('image_data_url')
    expect(request).not.toHaveProperty('reference_image_url')
    wrapper.unmount()
  })

  it.each(['http://media.example.com/a.png', 'javascript:alert(1)', 'not-a-url', 'https://user:secret@media.example.com/a.png', 'https://media.example.com/a.png#fragment'])('blocks invalid reference URL %s before submission', async url => {
    getAvailableModels.mockResolvedValue([{ id: 'MiniMax-H3', display_name: 'H3', video_endpoints: ['minimax'] }])
    const wrapper = await open()
    const vm = wrapper.vm as any
    await wrapper.find('[data-testid="account-test-reference-image-url"]').setValue(url)
    expect(vm.canStartTest).toBe(false)
    expect(wrapper.text()).toContain('admin.accounts.video.testReferenceImageURLInvalid')
    await vm.startTest()
    expect(fetch).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('keeps MiniMax image uploads available and mutually exclusive with the reference URL', async () => {
    getAvailableModels.mockResolvedValue([{ id: 'MiniMax-H3', display_name: 'H3', video_endpoints: ['minimax'] }])
    const wrapper = await open()
    const vm = wrapper.vm as any
    vm.videoReferenceImageTransport = 'https_url'
    const urlInput = wrapper.find('[data-testid="account-test-reference-image-url"]')
    await urlInput.setValue('https://media.example.com/first-frame.png')
    const file = new File([new Uint8Array([137, 80, 78, 71])], 'reference.png', { type: 'image/png' })
    await vm.onImageFileChange({ target: { files: [file], value: 'selected' } })
    await flushPromises()
    expect(vm.videoReferenceImageURL).toBe('')
    expect(vm.uploadImageDataURL).toMatch(/^data:image\/png;base64,/)
    await vm.startTest()
    const uploaded = JSON.parse((fetch as any).mock.calls[0][1].body)
    expect(uploaded.image_data_url).toBe(vm.uploadImageDataURL)
    expect(uploaded.reference_image_transport).toBe('https_url')
    expect(uploaded).not.toHaveProperty('reference_image_url')
    await urlInput.setValue('https://media.example.com/replacement.png')
    expect(vm.uploadImageDataURL).toBe('')
    expect(vm.uploadImageName).toBe('')
    await vm.startTest()
    const linked = JSON.parse((fetch as any).mock.calls[1][1].body)
    expect(linked.reference_image_url).toBe('https://media.example.com/replacement.png')
    expect(linked).not.toHaveProperty('image_data_url')
    expect(linked).not.toHaveProperty('reference_image_transport')
    wrapper.unmount()
  })

  it('invalidates an unfinished MiniMax upload when a reference URL is entered', async () => {
    getAvailableModels.mockResolvedValue([{ id: 'MiniMax-H3', display_name: 'H3', video_endpoints: ['minimax'] }])
    let completeRead: (() => void) | undefined
    vi.stubGlobal('FileReader', class {
      result = 'data:image/png;base64,AAAA'
      onload: (() => void) | null = null
      readAsDataURL() { completeRead = () => this.onload?.() }
    })
    const wrapper = await open()
    const vm = wrapper.vm as any
    const loading = vm.onImageFileChange({ target: { files: [new File(['png'], 'reference.png', { type: 'image/png' })], value: 'selected' } })
    expect(vm.loadingUpload).toBe(true)
    await wrapper.find('[data-testid="account-test-reference-image-url"]').setValue('https://media.example.com/first-frame.png')
    completeRead!()
    await loading
    expect(vm.uploadImageDataURL).toBe('')
    expect(vm.loadingUpload).toBe(false)
    await vm.startTest()
    expect(JSON.parse((fetch as any).mock.calls[0][1].body)).not.toHaveProperty('image_data_url')
    wrapper.unmount()
  })

  it.each(VIDEO_ENDPOINTS.filter(endpoint => endpoint !== 'minimax'))('clears and isolates MiniMax-only fields when switching to %s', async endpoint => {
    getAvailableModels.mockResolvedValue([{ id: 'video-model', display_name: 'Video', video_endpoints: ['minimax', endpoint] }])
    const wrapper = await open()
    const vm = wrapper.vm as any
    vm.videoEndpoint = 'minimax'
    await flushPromises()
    vm.videoReferenceImageURL = 'https://media.example.com/first-frame.png'
    vm.videoReferenceImageTransport = 'https_url'
    vm.videoAigcWatermark = 'on'
    vm.videoEndpoint = endpoint
    await flushPromises()
    expect(vm.videoReferenceImageURL).toBe('')
    expect(vm.videoReferenceImageTransport).toBe('data_url')
    expect(vm.videoAigcWatermark).toBe('unset')
    expect(wrapper.find('[data-testid="account-test-reference-image-url"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="account-test-watermark"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="account-test-reference-image-transport"]').exists()).toBe(false)
    // 即使程序误写隐藏状态，正式发送仍必须按当前协议拦住专属字段。
    vm.videoReferenceImageURL = 'https://media.example.com/stale.png'
    vm.videoReferenceImageTransport = 'https_url'
    vm.uploadImageDataURL = 'data:image/png;base64,AAAA'
    vm.videoAigcWatermark = 'on'
    await vm.startTest()
    const request = JSON.parse((fetch as any).mock.calls[0][1].body)
    expect(request).not.toHaveProperty('reference_image_url')
    expect(request).not.toHaveProperty('aigc_watermark')
    expect(request).not.toHaveProperty('reference_image_transport')
    wrapper.unmount()
  })

  it('clears MiniMax-only fields when reopening or changing accounts', async () => {
    getAvailableModels.mockResolvedValue([{ id: 'MiniMax-H3', display_name: 'H3', video_endpoints: ['minimax'] }])
    const wrapper = await open()
    const vm = wrapper.vm as any
    vm.videoReferenceImageURL = 'https://media.example.com/first-frame.png'
    vm.videoReferenceImageTransport = 'https_url'
    vm.videoAigcWatermark = 'off'
    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(vm.videoReferenceImageURL).toBe('')
    expect(vm.videoReferenceImageTransport).toBe('data_url')
    expect(vm.videoAigcWatermark).toBe('unset')
    vm.videoReferenceImageURL = 'https://media.example.com/next.png'
    vm.videoReferenceImageTransport = 'https_url'
    vm.videoAigcWatermark = 'on'
    await wrapper.setProps({ account: { id: 2, platform: 'video', type: 'apikey', credentials: { video_endpoints: ['minimax'] } } as Account })
    await flushPromises()
    expect(vm.videoReferenceImageURL).toBe('')
    expect(vm.videoReferenceImageTransport).toBe('data_url')
    expect(vm.videoAigcWatermark).toBe('unset')
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
