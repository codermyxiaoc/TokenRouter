import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import VideoAccountFields from '../VideoAccountFields.vue'
import { createVideoAccountForm, videoAccountCredentials, type VideoAccountForm } from '../videoAccountConfig'
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('Video account endpoint rows', () => {
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
