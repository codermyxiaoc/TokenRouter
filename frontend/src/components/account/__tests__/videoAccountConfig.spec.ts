import { describe, expect, it } from 'vitest'
import { createVideoAccountForm, validateVideoAccountForm, videoAccountCredentials } from '../videoAccountConfig'

describe('Video account credentials', () => {
  // 缺少端点配置时保持空选，不能通过初始化或保存隐式开启兼容入口。
  it.each([{}, { video_endpoints: [] }, { video_endpoints: null }])('requires explicit endpoint selection for %j', credentials => {
    const form = createVideoAccountForm(credentials)
    expect(form.endpoints).toEqual([])
    expect(validateVideoAccountForm(form, 'https://video.test')).toBe('endpointsRequired')
    expect(videoAccountCredentials(form).video_endpoints).toEqual([])
  })

  // 已明确保存的兼容端点保留，不能自动增加另一种 OpenAI 视频协议。
  it('preserves explicitly saved legacy compat accounts', () => {
    const credentials = {
      video_endpoints: ['compat'],
      video_base_urls: { compat: 'https://legacy.test' },
      video_model_bindings: { existing: 'compat' },
    }
    expect(videoAccountCredentials(createVideoAccountForm(credentials))).toMatchObject(credentials)
  })

  // 同时保存两种 OpenAI 端点及供应商原生端点，保留各自地址和同模型绑定。
  it('round-trips OpenAI Videos with compat and native endpoint bindings', () => {
    const credentials = {
      video_endpoints: ['compat', 'openai_videos', 'seedance', 'kling'],
      video_base_urls: { compat: 'https://legacy.test', openai_videos: 'https://openai-video.test/v1', seedance: 'https://ark.test/api/v3', kling: 'https://kling.test' },
      video_model_bindings: { legacy: 'compat', native: 'openai_videos', shared: ['compat', 'openai_videos', 'seedance', 'kling'] },
      video_model_paths: { shared: '/omni-video/{model}' },
      video_max_pending_tasks: 8,
      video_max_duration_seconds: 20,
    }
    const form = createVideoAccountForm(credentials)
    expect(form.endpoints).toEqual(credentials.video_endpoints)
    expect(form.bindings.filter(binding => binding.endpoint === 'openai_videos')).toEqual([
      { model: 'native', endpoint: 'openai_videos', path: '' },
      { model: 'shared', endpoint: 'openai_videos', path: '' },
    ])
    expect(validateVideoAccountForm(form, '')).toBeNull()
    expect(videoAccountCredentials(form)).toEqual({ ...credentials, video_openai_profile: 'passthrough' })
  })

  // 历史配置不自动转换参数；供应商适配是显式选择，暂时停用端点也不会丢失。
  it('defaults to passthrough and round-trips an explicit H3 profile while its endpoint is disabled', () => {
    const form = createVideoAccountForm({ video_endpoints: ['openai_videos'] })
    expect(form.openAIProfile).toBe('passthrough')
    const h3 = createVideoAccountForm({ video_endpoints: ['compat'], video_openai_profile: 'metaso_h3' })
    expect(h3.openAIProfile).toBe('metaso_h3')
    expect(validateVideoAccountForm(h3, 'https://video.test')).toBeNull()
    expect(videoAccountCredentials(h3).video_openai_profile).toBe('metaso_h3')
    h3.openAIProfile = 'passthrough'
    expect(videoAccountCredentials(h3).video_openai_profile).toBe('passthrough')
  })

  // 编辑旧账号时，域名或模型名称不能触发适配；空配置保存后仍使用原保真协议。
  it.each([undefined, null, '', ' ', 'passthrough'])('keeps a legacy H3-named account passthrough after editing profile %s', profile => {
    const credentials = {
      base_url: 'https://metaso.cn/api/openai',
      video_endpoints: ['compat', 'openai_videos'],
      video_openai_profile: profile,
      video_model_bindings: { 'MiniMax-H3': ['compat', 'openai_videos'] },
      video_max_pending_tasks: 12,
      video_max_duration_seconds: 20,
    }
    const form = createVideoAccountForm(credentials)
    expect(form.openAIProfile).toBe('passthrough')
    expect(validateVideoAccountForm(form, credentials.base_url)).toBeNull()
    const saved = videoAccountCredentials(form)
    expect(saved).toMatchObject({
      video_openai_profile: 'passthrough',
      video_endpoints: credentials.video_endpoints,
      video_model_bindings: credentials.video_model_bindings,
      video_max_pending_tasks: 12,
      video_max_duration_seconds: 20,
    })
    expect(createVideoAccountForm(saved).openAIProfile).toBe('passthrough')
  })

  it('rejects an unknown persisted profile instead of silently changing it', () => {
    const form = createVideoAccountForm({ video_endpoints: ['openai_videos'], video_openai_profile: 'future_profile' })
    expect(validateVideoAccountForm(form, 'https://video.test')).toBe('openAIProfileInvalid')
  })

  // 未绑定模型继承账号端点；只有同一模型和端点的重复行才非法。
  it('allows inherited multi-endpoint routing and requires endpoint URLs', () => {
    const form = createVideoAccountForm({ video_endpoints: ['seedance', 'kling'] })
    expect(validateVideoAccountForm(form, '')).toBe('baseUrlRequired')
    expect(validateVideoAccountForm(form, 'https://upstream.test')).toBeNull()
    form.bindings = [{ model: 'model', endpoint: 'kling', path: '/text-to-video/{model}' }]
    expect(validateVideoAccountForm(form, 'https://upstream.test')).toBeNull()
    form.bindings.push({ ...form.bindings[0] })
    expect(validateVideoAccountForm(form, 'https://upstream.test')).toBe('bindingDuplicate')
  })

  it('round-trips mixed legacy and multi-endpoint bindings without losing Kling paths', () => {
    const credentials = {
      video_endpoints: ['compat', 'seedance', 'kling'],
      video_model_bindings: { legacy: 'seedance', shared: ['kling', 'compat', 'seedance'] },
      video_model_paths: { shared: '/omni-video/{model}', inherited: '/v1/videos/omni-video' },
    }
    const form = createVideoAccountForm(credentials)
    expect(form.bindings).toEqual([
      { model: 'legacy', endpoint: 'seedance', path: '' },
      { model: 'shared', endpoint: 'kling', path: '/omni-video/{model}' },
      { model: 'shared', endpoint: 'compat', path: '' },
      { model: 'shared', endpoint: 'seedance', path: '' },
    ])
    expect(validateVideoAccountForm(form, 'https://video.test')).toBeNull()
    expect(videoAccountCredentials(form)).toMatchObject(credentials)
    // 移除同模型的非 Kling 行不改变其操作路径，也不会生成无绑定模型的端点限制。
    form.bindings = form.bindings.filter(binding => binding.endpoint !== 'compat' && (binding.model !== 'shared' || binding.endpoint === 'kling'))
    expect(videoAccountCredentials(form)).toMatchObject({
      video_model_bindings: { legacy: 'seedance', shared: 'kling' },
      video_model_paths: credentials.video_model_paths,
    })
  })

  it('preserves standalone paths while leaving models unbound', () => {
    const credentials = { video_endpoints: ['seedance', 'kling'], video_model_paths: { inherited: '/omni-video/{model}' } }
    const form = createVideoAccountForm(credentials)
    expect(form.bindings).toEqual([])
    expect(videoAccountCredentials(form)).toMatchObject({ video_model_bindings: {}, video_model_paths: credentials.video_model_paths })
  })

  it.each([
    { bindings: [{ model: ' ', endpoint: 'compat' as const, path: '' }], error: 'bindingModelRequired' },
    { bindings: [{ model: 'm', endpoint: 'compat' as const, path: '' }, { model: ' m ', endpoint: 'compat' as const, path: '' }], error: 'bindingDuplicate' },
    { bindings: [{ model: 'm', endpoint: 'wan' as const, path: '' }], error: 'bindingEndpointDisabled' },
    { bindings: [{ model: 'm', endpoint: 'kling' as const, path: '/unsupported' }], error: 'bindingPathInvalid' },
  ])('reports the concrete binding error $error', ({ bindings, error }) => {
    const form = createVideoAccountForm({ video_endpoints: ['compat', 'kling'] })
    form.bindings = bindings
    expect(validateVideoAccountForm(form, 'https://video.test')).toBe(error)
  })

  it.each([0, 1001, 1.5])('rejects invalid pending limit %s', value => {
    const form = createVideoAccountForm({ video_endpoints: ['compat'] })
    form.maxPendingTasks = value
    expect(validateVideoAccountForm(form, 'https://upstream.test')).toBe('pendingInvalid')
  })

  // 旧 Token 预算不再回填或写出，自动时长限制仍独立校验。
  it.each([undefined, null, '', 0, -1, 120000])('ignores removed Token limit %s without serializing it', value => {
    const form = createVideoAccountForm({ video_endpoints: ['compat'], video_max_output_tokens: value, video_max_duration_seconds: 15 })
    expect(form).not.toHaveProperty('maxOutputTokens')
    expect(validateVideoAccountForm(form, 'https://video.test')).toBeNull()
    expect(videoAccountCredentials(form)).not.toHaveProperty('video_max_output_tokens')
    expect(videoAccountCredentials(form)).toHaveProperty('video_max_duration_seconds', 15)
    form.maxDurationSeconds = 0
    expect(validateVideoAccountForm(form, 'https://video.test')).toBe('budgetInvalid')
  })

  it('preserves endpoint overrides and clears optional duration explicitly', () => {
    const form = createVideoAccountForm({ video_endpoints: ['wan'], video_base_urls: { wan: 'https://wan.test' }, video_max_duration_seconds: 20 })
    expect(validateVideoAccountForm(form, '')).toBeNull()
    form.maxDurationSeconds = ''
    const credentials = videoAccountCredentials(form)
    expect(credentials).toMatchObject({ video_base_urls: { wan: 'https://wan.test' }, video_model_bindings: {}, video_max_duration_seconds: null })
    expect(credentials).not.toHaveProperty('api_key')
    expect(credentials).not.toHaveProperty('video_max_output_tokens')
    form.maxDurationSeconds = -1
    expect(validateVideoAccountForm(form, '')).toBe('budgetInvalid')
  })
})
