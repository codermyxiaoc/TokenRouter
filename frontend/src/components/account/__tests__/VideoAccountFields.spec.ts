import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import VideoAccountFields from '../VideoAccountFields.vue'
import { VIDEO_ENDPOINTS, VIDEO_DEFAULT_ADAPTERS, createVideoAccountForm, validateVideoAccountForm, videoAccountCredentials, type VideoAccountForm } from '../videoAccountConfig'
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('Video account endpoint rows', () => {
  it('shows Kemei labels for compatibility endpoints and saves Vidu paths independently', async () => {
    const wrapper = mount(VideoAccountFields, { props: { modelValue: createVideoAccountForm({
      video_endpoints: ['compat', 'kling'], video_model_bindings: { shared: 'kling' },
      video_model_paths: { shared: '/omni-video/{model}' },
    }) }, global: { stubs: { Select: true } } })
    const acceptUpdate = async () => wrapper.setProps({ modelValue: wrapper.emitted('update:modelValue')!.at(-1)![0] as VideoAccountForm })
    const select = (id: string) => wrapper.findAllComponents({ name: 'Select' }).find(item => item.attributes('data-testid') === id)!
    expect(select('video-endpoint-adapter-compat').props('options')).toContainEqual({ value: 'kemei_v1', label: 'admin.accounts.video.adapters.kemei_v1' })
    select('video-endpoint-adapter-compat').vm.$emit('update:modelValue', 'kemei_v1')
    await acceptUpdate()
    await wrapper.get('[data-testid="video-endpoint-vidu"]').setValue(true)
    await acceptUpdate()
    expect(select('video-endpoint-adapter-vidu').props('modelValue')).toBe('vidu_v2')
    expect(select('video-endpoint-adapter-vidu').props('options')).toEqual([
      { value: 'vidu_v2', label: 'admin.accounts.video.adapters.vidu_v2' },
      { value: 'kemei_v1', label: 'admin.accounts.video.adapters.kemei_v1' },
      { value: 'legacy_v1', label: 'admin.accounts.video.adapters.legacy_v1' },
    ])
    await wrapper.get('[data-testid="video-add-binding"]').trigger('click')
    await acceptUpdate()
    await wrapper.get('[data-testid="video-binding-model-1"]').setValue('shared')
    await acceptUpdate()
    select('video-binding-endpoint-1').vm.$emit('update:modelValue', 'vidu')
    await acceptUpdate()
    expect(select('video-binding-path-1').props('options').map((item: { value: string }) => item.value)).toEqual(['', '/ent/v2/img2video', '/ent/v2/reference2video', '/ent/v2/text2video', '/ent/v2/start-end2video'])
    select('video-binding-path-1').vm.$emit('update:modelValue', '/ent/v2/reference2video')
    await acceptUpdate()
    expect(videoAccountCredentials(wrapper.props('modelValue'))).toMatchObject({
      video_model_paths: { shared: '/omni-video/{model}' }, video_vidu_model_paths: { shared: '/ent/v2/reference2video' },
      video_endpoint_adapters: { compat: 'kemei_v1', vidu: 'vidu_v2' },
      video_model_bindings: { shared: ['kling', 'vidu'] },
    })
    // 不同路径协议之间切换时清空旧路径，不能把 Vidu 操作带到 Kling。
    select('video-binding-endpoint-1').vm.$emit('update:modelValue', 'kling')
    await acceptUpdate()
    expect(select('video-binding-path-1').props('modelValue')).toBe('')
    expect(wrapper.find('select').exists()).toBe(false)
    wrapper.unmount()
  })

  it('uses the same legacy label for every endpoint and distinguishes the compatibility OpenAI format', async () => {
    const wrapper = mount(VideoAccountFields, { props: { modelValue: { ...createVideoAccountForm(), endpoints: [...VIDEO_ENDPOINTS] } }, global: { stubs: { Select: true } } })
    const select = (id: string) => wrapper.findAllComponents({ name: 'Select' }).find(item => item.attributes('data-testid') === id)!
    for (const endpoint of VIDEO_ENDPOINTS) {
      expect(select(`video-endpoint-adapter-${endpoint}`).props('options')).toContainEqual({ value: 'legacy_v1', label: 'admin.accounts.video.adapters.legacy_v1' })
    }
    expect(select('video-endpoint-adapter-compat').props('options')).toEqual([
      { value: 'legacy_v1', label: 'admin.accounts.video.adapters.legacy_v1' },
      { value: 'openai_v1', label: 'admin.accounts.video.adapters.compat_openai' },
      { value: 'kemei_v1', label: 'admin.accounts.video.adapters.kemei_v1' },
    ])
    expect(select('video-endpoint-adapter-openai_videos').props('options')).toContainEqual({ value: 'openai_v1', label: 'admin.accounts.video.adapters.openai_v1' })
    select('video-endpoint-adapter-compat').vm.$emit('update:modelValue', 'openai_v1')
    await wrapper.setProps({ modelValue: wrapper.emitted('update:modelValue')!.at(-1)![0] as VideoAccountForm })
    expect(wrapper.text()).toContain('admin.accounts.video.adapterHints.compat_openai')
    expect(videoAccountCredentials(wrapper.props('modelValue')).video_endpoint_adapters).toMatchObject({ compat: 'openai_v1' })
    wrapper.unmount()
  })

  it('does not display an implicit Vidu legacy selection when the saved adapter is missing', async () => {
    const wrapper = mount(VideoAccountFields, { props: { modelValue: createVideoAccountForm({ video_endpoints: ['vidu'] }) }, global: { stubs: { Select: true } } })
    const select = wrapper.findAllComponents({ name: 'Select' }).find(item => item.attributes('data-testid') === 'video-endpoint-adapter-vidu')!
    expect(select.props('modelValue')).toBe('')
    expect(select.props('placeholder')).toBe('admin.accounts.video.adapterPlaceholder')
    expect(validateVideoAccountForm(wrapper.props('modelValue'), 'https://video.test')).toBe('adapterInvalid')
    select.vm.$emit('update:modelValue', 'legacy_v1')
    await wrapper.setProps({ modelValue: wrapper.emitted('update:modelValue')!.at(-1)![0] as VideoAccountForm })
    expect(validateVideoAccountForm(wrapper.props('modelValue'), 'https://video.test')).toBeNull()
    expect(videoAccountCredentials(wrapper.props('modelValue')).video_endpoint_adapters).toEqual({ vidu: 'legacy_v1' })
    wrapper.unmount()
  })

  it.each(['kemei_v1', 'legacy_v1', 'vidu_v2'])('keeps saved Vidu scheme %s across disable and re-enable', async adapter => {
    const wrapper = mount(VideoAccountFields, { props: { modelValue: createVideoAccountForm({ video_endpoints: ['vidu'], video_endpoint_adapters: { vidu: adapter } }) }, global: { stubs: { Select: true } } })
    for (const checked of [false, true]) {
      await wrapper.get('[data-testid="video-endpoint-vidu"]').setValue(checked)
      await wrapper.setProps({ modelValue: wrapper.emitted('update:modelValue')!.at(-1)![0] as VideoAccountForm })
      expect(videoAccountCredentials(wrapper.props('modelValue')).video_endpoint_adapters).toEqual({ vidu: adapter })
    }
    wrapper.unmount()
  })

  // 每个已选端点都有独立选择框，反复启停不重置选择，不提供跨端点适配器。
  it('shows each enabled endpoint adapter and preserves it across endpoint toggles', async () => {
    const wrapper = mount(VideoAccountFields, { props: { modelValue: createVideoAccountForm({ video_endpoints: ['compat'] }) }, global: { stubs: { Select: true } } })
    const acceptUpdate = async () => wrapper.setProps({ modelValue: wrapper.emitted('update:modelValue')!.at(-1)![0] as VideoAccountForm })
    expect(wrapper.find('[data-testid="video-endpoint-adapter-compat"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="video-endpoint-adapter-openai_videos"]').exists()).toBe(false)
    await wrapper.get('[data-testid="video-endpoint-openai_videos"]').setValue(true)
    await acceptUpdate()
    const profile = wrapper.findAllComponents({ name: 'Select' }).find(select => select.attributes('data-testid') === 'video-endpoint-adapter-openai_videos')!
    expect(profile.props('modelValue')).toBe('legacy_v1')
    expect(profile.props('options').map((option: { value: string }) => option.value)).toEqual(['openai_v1', 'metaso_h3_v1', 'legacy_v1'])
    profile.vm.$emit('update:modelValue', 'metaso_h3_v1')
    await acceptUpdate()
    await wrapper.get('[data-testid="video-endpoint-openai_videos"]').setValue(false)
    await acceptUpdate()
    expect(wrapper.find('[data-testid="video-endpoint-adapter-openai_videos"]').exists()).toBe(false)
    expect(videoAccountCredentials(wrapper.props('modelValue')).video_openai_profile).toBe('metaso_h3')
    await wrapper.get('[data-testid="video-endpoint-openai_videos"]').setValue(true)
    await acceptUpdate()
    expect(wrapper.findAllComponents({ name: 'Select' }).find(select => select.attributes('data-testid') === 'video-endpoint-adapter-openai_videos')!.props('modelValue')).toBe('metaso_h3_v1')
    expect(wrapper.find('select').exists()).toBe(false)
    wrapper.unmount()
  })

  it('shows official defaults for new accounts and allows independent model inheritance', async () => {
    const wrapper = mount(VideoAccountFields, { props: { modelValue: { ...createVideoAccountForm(), endpoints: [...VIDEO_ENDPOINTS] } }, global: { stubs: { Select: true } } })
    const acceptUpdate = async () => wrapper.setProps({ modelValue: wrapper.emitted('update:modelValue')!.at(-1)![0] as VideoAccountForm })
    const select = (id: string) => wrapper.findAllComponents({ name: 'Select' }).find(item => item.attributes('data-testid') === id)!
    for (const endpoint of VIDEO_ENDPOINTS) expect(select(`video-endpoint-adapter-${endpoint}`).props('modelValue')).toBe(VIDEO_DEFAULT_ADAPTERS[endpoint])
    await wrapper.get('[data-testid="video-add-binding"]').trigger('click')
    await acceptUpdate()
    await wrapper.get('[data-testid="video-binding-model-0"]').setValue('shared')
    await acceptUpdate()
    select('video-binding-endpoint-0').vm.$emit('update:modelValue', 'openai_videos')
    await acceptUpdate()
    expect(select('video-binding-adapter-0').props('modelValue')).toBe('')
    select('video-binding-adapter-0').vm.$emit('update:modelValue', 'metaso_h3_v1')
    await acceptUpdate()
    expect(videoAccountCredentials(wrapper.props('modelValue')).video_model_adapters).toEqual({ shared: { openai_videos: 'metaso_h3_v1' } })
    expect(select('video-endpoint-adapter-openai_videos').props('modelValue')).toBe('openai_v1')
    // 改端点应清掉不适用的旧覆盖，删除绑定后也不得保留隐藏的模型配置。
    select('video-binding-endpoint-0').vm.$emit('update:modelValue', 'minimax')
    await acceptUpdate()
    expect(select('video-binding-adapter-0').props('modelValue')).toBe('')
    expect(select('video-binding-adapter-0').props('options').map((item: { value: string }) => item.value)).toEqual(['', 'minimax_v2', 'kemei_v1', 'legacy_v1'])
    select('video-binding-adapter-0').vm.$emit('update:modelValue', 'legacy_v1')
    await acceptUpdate()
    await wrapper.get('[data-testid="video-remove-binding-0"]').trigger('click')
    await acceptUpdate()
    expect(videoAccountCredentials(wrapper.props('modelValue')).video_model_adapters).toEqual({})
    expect(wrapper.find('select').exists()).toBe(false)
    wrapper.unmount()
  })

  // 初始为空时不能添加隐式 compat 绑定，勾选原生协议后只使用该明确选择。
  it('leaves compatibility unchecked and requires an endpoint before adding bindings', async () => {
    const wrapper = mount(VideoAccountFields, { props: { modelValue: createVideoAccountForm() }, global: { stubs: { Select: true } } })
    const acceptUpdate = async () => wrapper.setProps({ modelValue: wrapper.emitted('update:modelValue')!.at(-1)![0] as VideoAccountForm })
    expect((wrapper.get('[data-testid="video-endpoint-compat"]').element as HTMLInputElement).checked).toBe(false)
    expect((wrapper.get('[data-testid="video-endpoint-openai_videos"]').element as HTMLInputElement).checked).toBe(false)
    expect((wrapper.get('[data-testid="video-add-binding"]').element as HTMLButtonElement).disabled).toBe(true)
    await wrapper.get('[data-testid="video-add-binding"]').trigger('click')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    await wrapper.get('[data-testid="video-endpoint-seedance"]').setValue(true)
    await acceptUpdate()
    await wrapper.get('[data-testid="video-add-binding"]').trigger('click')
    await acceptUpdate()
    expect(wrapper.props('modelValue').endpoints).toEqual(['seedance'])
    expect(wrapper.props('modelValue').bindings).toEqual([{ model: '', endpoint: 'seedance', path: '' }])
    wrapper.unmount()
  })

  // 从历史账号勾选新端点，再通过自研选择框绑定同一模型，旧配置必须完整保留。
  it('adds OpenAI Videos alongside compat and Seedance with a separate URL and model binding', async () => {
    const wrapper = mount(VideoAccountFields, { props: { modelValue: createVideoAccountForm({
      video_endpoints: ['compat', 'seedance'],
      video_base_urls: { compat: 'https://legacy.test', seedance: 'https://ark.test/api/v3' },
      video_model_bindings: { shared: ['compat', 'seedance'] },
    }) }, global: { stubs: { Select: true } } })
    const acceptUpdate = async () => wrapper.setProps({ modelValue: wrapper.emitted('update:modelValue')!.at(-1)![0] as VideoAccountForm })
    expect((wrapper.get('[data-testid="video-endpoint-openai_videos"]').element as HTMLInputElement).checked).toBe(false)
    await wrapper.get('[data-testid="video-endpoint-openai_videos"]').setValue(true)
    await acceptUpdate()
    await wrapper.get('[data-testid="video-endpoint-url-openai_videos"]').setValue('https://openai-video.test/v1')
    await acceptUpdate()
    await wrapper.get('[data-testid="video-add-binding"]').trigger('click')
    await acceptUpdate()
    await wrapper.get('[data-testid="video-binding-model-2"]').setValue('shared')
    await acceptUpdate()
    const endpoint = wrapper.findAllComponents({ name: 'Select' }).find(select => select.attributes('data-testid') === 'video-binding-endpoint-2')!
    expect(endpoint.props('options')).toContainEqual({ value: 'openai_videos', label: 'admin.accounts.video.endpoints.openai_videos' })
    endpoint.vm.$emit('update:modelValue', 'openai_videos')
    await acceptUpdate()
    expect(videoAccountCredentials(wrapper.props('modelValue'))).toMatchObject({
      video_endpoints: ['compat', 'seedance', 'openai_videos'],
      video_base_urls: { compat: 'https://legacy.test', seedance: 'https://ark.test/api/v3', openai_videos: 'https://openai-video.test/v1' },
      video_model_bindings: { shared: ['compat', 'seedance', 'openai_videos'] },
    })
    expect(wrapper.find('select').exists()).toBe(false)
    // 取消新端点时只删除对应绑定，保存仍保留原有两个端点与地址。
    await wrapper.get('[data-testid="video-endpoint-openai_videos"]').setValue(false)
    await acceptUpdate()
    expect(videoAccountCredentials(wrapper.props('modelValue'))).toMatchObject({
      video_endpoints: ['compat', 'seedance'],
      video_base_urls: { compat: 'https://legacy.test', seedance: 'https://ark.test/api/v3' },
      video_model_bindings: { shared: ['compat', 'seedance'] },
    })
    wrapper.unmount()
  })

  // 账号仅保留自动时长上限，不再展示或提交历史 Token 上限。
  it('omits the removed Token budget while preserving automatic duration', () => {
    const wrapper = mount(VideoAccountFields, { props: { modelValue: createVideoAccountForm({ video_max_output_tokens: 120000, video_max_duration_seconds: 15 }) }, global: { stubs: { Select: true } } })
    expect(wrapper.find('[data-testid="video-max-output-tokens"]').exists()).toBe(false)
    expect(videoAccountCredentials(wrapper.props('modelValue'))).not.toHaveProperty('video_max_output_tokens')
    expect(videoAccountCredentials(wrapper.props('modelValue'))).toHaveProperty('video_max_duration_seconds', 15)
    wrapper.unmount()
  })

  it('initializes a new Kling binding from its independent path but allows explicit clearing', async () => {
    const wrapper = mount(VideoAccountFields, { props: { modelValue: createVideoAccountForm({
      video_endpoints: ['kling', 'compat'], video_model_paths: { inherited: '/omni-video/{model}' },
    }) }, global: { stubs: { Select: true } } })
    const acceptUpdate = async () => wrapper.setProps({ modelValue: wrapper.emitted('update:modelValue')!.at(-1)![0] as VideoAccountForm })
    await wrapper.get('[data-testid="video-add-binding"]').trigger('click')
    await acceptUpdate()
    await wrapper.get('[data-testid="video-binding-model-0"]').setValue('inherited')
    await acceptUpdate()
    const path = wrapper.findAllComponents({ name: 'Select' }).find(select => select.attributes('data-testid') === 'video-binding-path-0')!
    expect(path.props('modelValue')).toBe('/omni-video/{model}')
    path.vm.$emit('update:modelValue', '')
    await acceptUpdate()
    expect(videoAccountCredentials(wrapper.props('modelValue')).video_model_paths).toEqual({})
    wrapper.unmount()
  })

  // 操作真实行组件，验证增删同模型端点时只修改指定行。
  it('adds another endpoint to one model without overwriting its Kling path', async () => {
    const wrapper = mount(VideoAccountFields, { props: { modelValue: createVideoAccountForm({
      video_endpoints: ['kling', 'compat', 'seedance'],
      video_model_bindings: { shared: 'kling' }, video_model_paths: { shared: '/omni-video/{model}' },
    }) }, global: { stubs: { Select: true } } })
    const acceptUpdate = async () => wrapper.setProps({ modelValue: wrapper.emitted('update:modelValue')!.at(-1)![0] as VideoAccountForm })
    await wrapper.get('[data-testid="video-add-binding"]').trigger('click')
    await acceptUpdate()
    await wrapper.get('[data-testid="video-binding-model-1"]').setValue('shared')
    await acceptUpdate()
    wrapper.findAllComponents({ name: 'Select' }).find(select => select.attributes('data-testid') === 'video-binding-endpoint-1')!.vm.$emit('update:modelValue', 'compat')
    await acceptUpdate()
    expect(videoAccountCredentials(wrapper.props('modelValue'))).toMatchObject({
      video_model_bindings: { shared: ['kling', 'compat'] },
      video_model_paths: { shared: '/omni-video/{model}' },
    })
    await wrapper.get('[data-testid="video-remove-binding-1"]').trigger('click')
    await acceptUpdate()
    expect(videoAccountCredentials(wrapper.props('modelValue'))).toMatchObject({
      video_model_bindings: { shared: 'kling' }, video_model_paths: { shared: '/omni-video/{model}' },
    })
    expect(wrapper.find('select').exists()).toBe(false)
    wrapper.unmount()
  })

  it('disabling one endpoint retains the other endpoints and their model paths', async () => {
    const wrapper = mount(VideoAccountFields, { props: { modelValue: createVideoAccountForm({
      video_endpoints: ['kling', 'compat'], video_model_bindings: { shared: ['kling', 'compat'] },
      video_model_paths: { shared: '/text-to-video/{model}', inherited: '/omni-video/{model}' },
    }) }, global: { stubs: { Select: true } } })
    await wrapper.get('[data-testid="video-endpoint-compat"]').setValue(false)
    const next = wrapper.emitted('update:modelValue')!.at(-1)![0] as VideoAccountForm
    expect(next.endpoints).toEqual(['kling'])
    expect(videoAccountCredentials(next)).toMatchObject({
      video_model_bindings: { shared: 'kling' },
      video_model_paths: { shared: '/text-to-video/{model}', inherited: '/omni-video/{model}' },
    })
    wrapper.unmount()
  })
})
