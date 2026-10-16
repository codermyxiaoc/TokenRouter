import { describe, expect, it } from 'vitest'
import { VIDEO_ENDPOINTS, VIDEO_DEFAULT_ADAPTERS, VIDEO_ENDPOINT_ADAPTERS, VIDEO_VIDU_PATHS, createVideoAccountForm, validateVideoAccountForm, videoAccountCredentials, videoEndpointAdapter, type VideoEndpoint, type VideoProtocolAdapter } from '../videoAccountConfig'

describe('Video account credentials', () => {
  // 新增可选合同不替换已保存的可美端点；模型覆盖及继承继续独立往返。
  it.each<[VideoEndpoint, VideoProtocolAdapter]>([['compat', 'openai_v1'], ['vidu', 'vidu_v2'], ['vidu', 'legacy_v1']])('preserves %s adapter %s only when explicitly selected', (endpoint, adapter) => {
    const form = createVideoAccountForm({
      video_endpoints: [endpoint], video_endpoint_adapters: { [endpoint]: 'kemei_v1' },
      video_model_bindings: { mapped: endpoint },
      video_model_adapters: { mapped: { [endpoint]: adapter }, unbound: { [endpoint]: adapter } },
    })
    expect(validateVideoAccountForm(form, 'https://video.test')).toBeNull()
    expect(videoEndpointAdapter(form, endpoint)).toBe('kemei_v1')
    expect(videoAccountCredentials(form)).toMatchObject({
      video_endpoint_adapters: { [endpoint]: 'kemei_v1' },
      video_model_adapters: { mapped: { [endpoint]: adapter }, unbound: { [endpoint]: adapter } },
    })
    form.bindings[0].adapter = ''
    form.endpointAdapters[endpoint] = adapter
    const saved = videoAccountCredentials(form)
    expect(saved.video_model_adapters).toEqual({ unbound: { [endpoint]: adapter } })
    expect(videoEndpointAdapter(createVideoAccountForm(saved), endpoint)).toBe(adapter)
  })

  it('requires an explicit Vidu endpoint adapter even with a valid model override', () => {
    const form = createVideoAccountForm({
      video_endpoints: ['vidu'], video_model_bindings: { model: 'vidu' },
      video_model_adapters: { model: { vidu: 'vidu_v2' } },
    })
    expect(validateVideoAccountForm(form, 'https://video.test')).toBe('adapterInvalid')
    expect(form.endpointAdapters).toEqual({})
    form.endpointAdapters.vidu = 'legacy_v1'
    expect(validateVideoAccountForm(form, 'https://video.test')).toBeNull()
  })
  // 供应商方案只能显式选择，不因域名、模型名称或编辑旧账号而自动启用。
  it.each(['compat', 'seedance', 'minimax', 'wan', 'kling', 'vidu'] as const)('round-trips an explicit Kemei adapter and model override for %s', endpoint => {
    const credentials = {
      base_url: 'https://token.xinhankr.com', video_endpoints: [endpoint],
      video_endpoint_adapters: { [endpoint]: 'kemei_v1' },
      video_model_bindings: { shared: endpoint },
      video_model_adapters: { shared: { [endpoint]: 'kemei_v1' } },
    }
    const form = createVideoAccountForm(credentials)
    expect(validateVideoAccountForm(form, credentials.base_url)).toBeNull()
    expect(videoEndpointAdapter(form, endpoint)).toBe('kemei_v1')
    expect(videoAccountCredentials(form)).toMatchObject({
      video_endpoints: [endpoint], video_endpoint_adapters: credentials.video_endpoint_adapters,
      video_model_adapters: credentials.video_model_adapters,
    })
    form.bindings[0].adapter = ''
    expect(videoAccountCredentials(form).video_model_adapters).toEqual({})
    expect(videoAccountCredentials(form).video_endpoint_adapters).toEqual(credentials.video_endpoint_adapters)
  })

  it('does not add unsupported Kemei OpenAI Videos or infer the provider from an account URL', () => {
    expect(VIDEO_ENDPOINT_ADAPTERS.openai_videos).not.toContain('kemei_v1')
    const form = createVideoAccountForm({ base_url: 'https://token.xinhankr.com', video_endpoints: ['compat', 'minimax'] })
    expect(form.endpointAdapters).toEqual({})
    expect(videoEndpointAdapter(form, 'minimax')).toBe('legacy_v1')
    expect(validateVideoAccountForm(createVideoAccountForm({ video_endpoints: ['openai_videos'], video_endpoint_adapters: { openai_videos: 'kemei_v1' } }), 'https://video.test')).toBe('adapterInvalid')
    expect(validateVideoAccountForm(createVideoAccountForm({ video_endpoints: ['vidu'] }), 'https://video.test')).toBe('adapterInvalid')
  })

  // 相同模型的 Kling、Vidu 路径必须独立保存，增删其中一个绑定不改写另一个。
  it.each(VIDEO_VIDU_PATHS)('preserves separate Kling and Vidu paths for %s', path => {
    const credentials = {
      video_endpoints: ['kling', 'vidu'], video_endpoint_adapters: { kling: 'kemei_v1', vidu: 'kemei_v1' },
      video_model_bindings: { shared: ['kling', 'vidu'] },
      video_model_paths: { shared: '/omni-video/{model}', old: '/text-to-video/{model}' },
      video_vidu_model_paths: { shared: path, inherited: '/ent/v2/img2video' },
    }
    const form = createVideoAccountForm(credentials)
    expect(form.bindings.map(row => row.path)).toEqual(['/omni-video/{model}', path])
    expect(validateVideoAccountForm(form, 'https://video.test')).toBeNull()
    expect(videoAccountCredentials(form)).toMatchObject(credentials)
    form.bindings = form.bindings.filter(row => row.endpoint !== 'vidu')
    expect(videoAccountCredentials(form)).toMatchObject({
      video_model_paths: credentials.video_model_paths, video_vidu_model_paths: { inherited: '/ent/v2/img2video' },
      video_model_bindings: { shared: 'kling' },
    })
  })

  it.each(['/tasks', 'https://evil.test/ent/v2/img2video', '/text-to-video/{model}'])('rejects an invalid Vidu operation path %s', path => {
    const form = createVideoAccountForm({ video_endpoints: ['vidu'], video_endpoint_adapters: { vidu: 'kemei_v1' }, video_model_bindings: { model: 'vidu' }, video_vidu_model_paths: { model: path } })
    expect(validateVideoAccountForm(form, 'https://video.test')).toBe('bindingPathInvalid')
    form.bindings = []
    form.unboundViduModelPaths = { inherited: path }
    expect(validateVideoAccountForm(form, 'https://video.test')).toBe('bindingPathInvalid')
  })
  // 创建与编辑必须显式区分，保存旧账号不能偷偷切换官方方案。
  it('uses official defaults only for a new form and preserves legacy behavior for saved empty credentials', () => {
    const created = createVideoAccountForm()
    expect(created.endpoints).toEqual([])
    expect(created.endpointAdapters).toEqual(VIDEO_DEFAULT_ADAPTERS)
    expect(created.endpointAdapters.compat).toBe('legacy_v1')
    expect(created.endpointAdapters.vidu).toBe('vidu_v2')
    const saved = createVideoAccountForm({})
    expect(saved.endpointAdapters).toEqual({})
    for (const endpoint of VIDEO_ENDPOINTS) expect(videoEndpointAdapter(saved, endpoint)).toBe('legacy_v1')
  })

  it('round-trips endpoint adapters and per-model overrides independently of endpoint authorization', () => {
    const credentials = {
      video_endpoints: ['openai_videos', 'minimax'],
      video_openai_profile: 'metaso_h3',
      video_endpoint_adapters: { openai_videos: 'openai_v1', minimax: 'minimax_v2', seedance: 'seedance_v1' },
      video_model_bindings: { shared: ['openai_videos', 'minimax'] },
      video_model_adapters: { shared: { openai_videos: 'metaso_h3_v1', minimax: 'legacy_v1' }, unbound: { openai_videos: 'legacy_v1' } },
    }
    const form = createVideoAccountForm(credentials)
    expect(videoEndpointAdapter(form, 'openai_videos')).toBe('openai_v1')
    expect(form.bindings.map(row => row.adapter)).toEqual(['metaso_h3_v1', 'legacy_v1'])
    expect(validateVideoAccountForm(form, 'https://video.test')).toBeNull()
    expect(videoAccountCredentials(form)).toMatchObject({ ...credentials, video_openai_profile: 'passthrough' })
    // 清除某条覆盖只恢复该端点继承，不删除同模型其他覆盖或隐式收窄未绑定模型。
    form.bindings[0].adapter = ''
    expect(videoAccountCredentials(form)).toMatchObject({
      video_model_bindings: credentials.video_model_bindings,
      video_model_adapters: { shared: { minimax: 'legacy_v1' }, unbound: { openai_videos: 'legacy_v1' } },
    })
    expect(videoAccountCredentials(form).video_model_adapters).toEqual({ shared: { minimax: 'legacy_v1' }, unbound: { openai_videos: 'legacy_v1' } })
    form.bindings = []
    expect(videoAccountCredentials(form).video_model_adapters).toEqual({ unbound: { openai_videos: 'legacy_v1' } })
  })

  it.each([
    { video_endpoint_adapters: { minimax: 'openai_v1' } },
    { video_endpoint_adapters: { minimax: 'vidu_v2' } },
    { video_endpoint_adapters: { unknown: 'legacy_v1' } },
    { video_endpoint_adapters: { openai_videos: 'future_v9' } },
    { video_endpoint_adapters: [] },
    { video_model_adapters: [] },
    { video_model_adapters: { model: [] } },
    { video_model_adapters: { model: { minimax: 'wan_v1' } } },
  ])('rejects invalid or cross-endpoint adapter configuration %j', extra => {
    const form = createVideoAccountForm({ video_endpoints: ['minimax'], ...extra })
    expect(validateVideoAccountForm(form, 'https://video.test')).toBe('adapterInvalid')
  })

  it('does not reactivate old H3 settings after explicitly choosing the original protocol', () => {
    const form = createVideoAccountForm({ video_endpoints: ['openai_videos'], video_openai_profile: 'metaso_h3' })
    expect(videoEndpointAdapter(form, 'openai_videos')).toBe('metaso_h3_v1')
    form.endpointAdapters.openai_videos = 'legacy_v1'
    const saved = videoAccountCredentials(form)
    expect(saved.video_openai_profile).toBe('passthrough')
    expect(videoEndpointAdapter(createVideoAccountForm(saved), 'openai_videos')).toBe('legacy_v1')
  })

  // 官方方案不能沿用中继旧路径；单模型保留旧协议时则不影响其他官方模型。
  it.each(['/v1/videos/text2video', '/omni-video/{model}'])('rejects unverified Kling path %s for the official adapter and honors model overrides', path => {
    const form = createVideoAccountForm({
      video_endpoints: ['kling'], video_endpoint_adapters: { kling: 'kling_v1' },
      video_model_bindings: { model: 'kling' }, video_model_paths: { model: path },
    })
    expect(validateVideoAccountForm(form, 'https://video.test')).toBe('adapterKlingPathInvalid')
    form.bindings[0].adapter = 'legacy_v1'
    expect(validateVideoAccountForm(form, 'https://video.test')).toBeNull()
    form.bindings[0].adapter = ''
    form.bindings[0].path = ''
    expect(validateVideoAccountForm(form, 'https://video.test')).toBeNull()
    form.unboundModelPaths = { inherited: '/v1/videos/omni-video' }
    expect(validateVideoAccountForm(form, 'https://video.test')).toBe('adapterKlingPathInvalid')
    form.unboundModelAdapters = { inherited: { kling: 'legacy_v1' } }
    expect(validateVideoAccountForm(form, 'https://video.test')).toBeNull()
  })

  // 可美只接现代操作，显式模型覆盖不能借旧路径绕过发送前校验。
  it.each(['/v1/videos/text2video', '/v1/videos/omni-video'])('rejects legacy Kling path %s for Kemei at endpoint and model scope', path => {
    const form = createVideoAccountForm({
      video_endpoints: ['kling'], video_endpoint_adapters: { kling: 'kemei_v1' },
      video_model_bindings: { model: 'kling' }, video_model_paths: { model: path },
    })
    expect(validateVideoAccountForm(form, 'https://video.test')).toBe('adapterKemeiKlingPathInvalid')
    form.bindings[0].adapter = 'legacy_v1'
    expect(validateVideoAccountForm(form, 'https://video.test')).toBeNull()
    form.endpointAdapters.kling = 'legacy_v1'
    form.bindings[0].adapter = 'kemei_v1'
    expect(validateVideoAccountForm(form, 'https://video.test')).toBe('adapterKemeiKlingPathInvalid')
    form.bindings = []
    form.unboundModelPaths = { inherited: path }
    form.unboundModelAdapters = { inherited: { kling: 'kemei_v1' } }
    expect(validateVideoAccountForm(form, 'https://video.test')).toBe('adapterKemeiKlingPathInvalid')
    form.endpointAdapters.kling = 'kemei_v1'
    form.unboundModelAdapters = { inherited: { kling: 'legacy_v1' } }
    expect(validateVideoAccountForm(form, 'https://video.test')).toBeNull()
    form.unboundModelAdapters = {}
    expect(validateVideoAccountForm(form, 'https://video.test')).toBe('adapterKemeiKlingPathInvalid')
  })

  it.each(['/text-to-video/{model}', '/image-to-video/{model}', '/omni-video/{model}'])('accepts modern Kling path %s for Kemei without a binding requirement', path => {
    const form = createVideoAccountForm({
      video_endpoints: ['kling'], video_endpoint_adapters: { kling: 'kemei_v1' },
      video_model_bindings: { model: 'kling' }, video_model_paths: { model: path, inherited: path },
    })
    expect(validateVideoAccountForm(form, 'https://video.test')).toBeNull()
    expect(videoAccountCredentials(form).video_model_paths).toEqual({ model: path, inherited: path })
  })

  it.each(['model-*', 'model-?'])('requires an exact model name for adapter override %s', model => {
    const form = createVideoAccountForm({ video_endpoints: ['minimax'] })
    form.bindings = [{ model, endpoint: 'minimax', path: '', adapter: 'minimax_v2' }]
    expect(validateVideoAccountForm(form, 'https://video.test')).toBe('adapterModelExactRequired')
    form.bindings[0].adapter = ''
    expect(validateVideoAccountForm(form, 'https://video.test')).toBeNull()
  })
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
    expect(videoAccountCredentials(form)).toEqual({ ...credentials, video_openai_profile: 'passthrough', video_endpoint_adapters: {}, video_model_adapters: {}, video_vidu_model_paths: {} })
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
