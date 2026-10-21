// 两种 OpenAI 视频路径是独立能力，保留旧 compat 标识，不自动迁移历史账号。
export const VIDEO_ENDPOINTS = ['compat', 'openai_videos', 'seedance', 'kling', 'wan', 'minimax', 'vidu'] as const
export type VideoEndpoint = typeof VIDEO_ENDPOINTS[number]
export const VIDEO_OPENAI_PROFILES = ['passthrough', 'metaso_h3'] as const
export type VideoOpenAIProfile = typeof VIDEO_OPENAI_PROFILES[number]
// 方案 ID 固定带版本；新增协议只能显式加入所属端点，不能借适配配置增加端点权限。
export const VIDEO_ENDPOINT_ADAPTERS = {
  compat: ['legacy_v1', 'openai_v1', 'kemei_v1'],
  openai_videos: ['openai_v1', 'metaso_h3_v1', 'yezonghui_v1', 'legacy_v1'],
  seedance: ['seedance_v1', 'kemei_v1', 'legacy_v1'],
  kling: ['kling_v1', 'kemei_v1', 'legacy_v1'],
  wan: ['wan_v1', 'kemei_v1', 'legacy_v1'],
  minimax: ['minimax_v2', 'kemei_v1', 'legacy_v1'],
  vidu: ['vidu_v2', 'kemei_v1', 'legacy_v1'],
} as const satisfies Record<VideoEndpoint, readonly string[]>
export type VideoProtocolAdapter = typeof VIDEO_ENDPOINT_ADAPTERS[VideoEndpoint][number]
export type VideoEndpointAdapters = Partial<Record<VideoEndpoint, VideoProtocolAdapter>>
export const VIDEO_DEFAULT_ADAPTERS: Record<VideoEndpoint, VideoProtocolAdapter> = {
  compat: 'legacy_v1', openai_videos: 'openai_v1', seedance: 'seedance_v1',
  kling: 'kling_v1', wan: 'wan_v1', minimax: 'minimax_v2', vidu: 'vidu_v2',
}
export const VIDEO_MODEL_PATHS = ['/text-to-video/{model}', '/image-to-video/{model}', '/omni-video/{model}', '/v1/videos/text2video', '/v1/videos/omni-video'] as const
// Vidu 操作与 Kling 路径分别保存，同模型的多端点配置不能互相覆盖。
export const VIDEO_VIDU_PATHS = ['/ent/v2/img2video', '/ent/v2/reference2video', '/ent/v2/text2video', '/ent/v2/start-end2video'] as const

export interface VideoModelBindingForm { model: string; endpoint: VideoEndpoint; path: string; adapter?: VideoProtocolAdapter | '' }
export interface VideoAccountForm {
  endpoints: VideoEndpoint[]
  openAIProfile: VideoOpenAIProfile
  endpointAdapters: VideoEndpointAdapters
  // 未显式绑定的模型覆盖只影响协议，不补模型绑定以免收窄原端点能力。
  unboundModelAdapters?: Record<string, VideoEndpointAdapters>
  invalidAdapterConfig?: boolean
  baseUrls: Partial<Record<VideoEndpoint, string>>
  bindings: VideoModelBindingForm[]
  // 没有显式 Kling 绑定的历史路径仍可能用于继承端点，不擅自新增绑定收窄能力。
  unboundModelPaths?: Record<string, string>
  unboundViduModelPaths?: Record<string, string>
  maxPendingTasks: number | string
  maxDurationSeconds: number | string | null
}

// @project-doc docs/interfaces/upstream_account_matrix.md#video_account
// 创建和编辑共用凭据契约；仅回填明确保存的端点，不默认启用兼容入口。
export function createVideoAccountForm(savedCredentials?: Record<string, unknown>): VideoAccountForm {
  // 创建无参，编辑显式传入凭据（包括空对象）；仅新建表单预选官方方案。
  const credentials = savedCredentials ?? {}
  const endpoints = Array.isArray(credentials.video_endpoints)
    ? credentials.video_endpoints.filter((value): value is VideoEndpoint => VIDEO_ENDPOINTS.includes(value as VideoEndpoint))
    : []
  const bindings = (credentials.video_model_bindings || {}) as Record<string, VideoEndpoint | VideoEndpoint[]>
  const paths = (credentials.video_model_paths || {}) as Record<string, string>
  const viduPaths = (credentials.video_vidu_model_paths || {}) as Record<string, string>
  const endpointAdapters = objectRecord(credentials.video_endpoint_adapters)
  const modelAdapters = objectRecord(credentials.video_model_adapters)
  const rows = Object.entries(bindings).flatMap(([model, value]) =>
    (Array.isArray(value) ? value : [value]).map(endpoint => {
      const adapter = objectRecord(modelAdapters?.[model])?.[endpoint]
      return { model, endpoint, path: endpoint === 'kling' ? paths[model] || '' : endpoint === 'vidu' ? viduPaths[model] || '' : '',
        ...(adapter !== undefined ? { adapter: adapter as VideoProtocolAdapter } : {}) }
    }))
  return {
    endpoints,
    // 默认不转换历史请求；未知配置保留给校验处理，不能静默切换供应商协议。
    openAIProfile: (String(credentials.video_openai_profile ?? '').trim() || 'passthrough') as VideoOpenAIProfile,
    endpointAdapters: savedCredentials === undefined ? { ...VIDEO_DEFAULT_ADAPTERS } : { ...endpointAdapters } as VideoEndpointAdapters,
    unboundModelAdapters: Object.fromEntries(Object.entries(modelAdapters ?? {}).map(([model, adapters]) => [model,
      Object.fromEntries(Object.entries(objectRecord(adapters) ?? {}).filter(([endpoint]) => !rows.some(row => row.model === model && row.endpoint === endpoint)))
    ]).filter(([, adapters]) => Object.keys(adapters as object).length)),
    invalidAdapterConfig: (credentials.video_endpoint_adapters != null && endpointAdapters === undefined)
      || (credentials.video_model_adapters != null && modelAdapters === undefined)
      || Object.values(modelAdapters ?? {}).some(value => !objectRecord(value)),
    baseUrls: { ...(credentials.video_base_urls || {}) as Partial<Record<VideoEndpoint, string>> },
    // 旧字符串和新数组统一展开成行；模型路径只属于 Kling 行，不能被其他协议行覆盖。
    bindings: rows,
    unboundModelPaths: Object.fromEntries(Object.entries(paths).filter(([model]) => {
      const value = bindings[model]
      return !(Array.isArray(value) ? value : [value]).includes('kling')
    })),
    unboundViduModelPaths: Object.fromEntries(Object.entries(viduPaths).filter(([model]) => {
      const value = bindings[model]
      return !(Array.isArray(value) ? value : [value]).includes('vidu')
    })),
    maxPendingTasks: Number(credentials.video_max_pending_tasks ?? 10),
    maxDurationSeconds: credentials.video_max_duration_seconds == null ? null : Number(credentials.video_max_duration_seconds),
  }
}

const objectRecord = (value: unknown): Record<string, unknown> | undefined =>
  value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : undefined

export function videoEndpointAdapter(form: VideoAccountForm, endpoint: VideoEndpoint): VideoProtocolAdapter {
  return form.endpointAdapters[endpoint]
    ?? (endpoint === 'openai_videos' && form.openAIProfile === 'metaso_h3' ? 'metaso_h3_v1' : 'legacy_v1')
}

// compat 仅采用 OpenAI 参数格式，不能将兼容路径误称为 OpenAI 官方端点。
export function videoAdapterTextKey(endpoint: VideoEndpoint | '', adapter: VideoProtocolAdapter): string {
  return endpoint === 'compat' && adapter === 'openai_v1' ? 'compat_openai' : adapter
}

export function isVideoEndpointAdapter(endpoint: string, adapter: unknown): adapter is VideoProtocolAdapter {
  return VIDEO_ENDPOINTS.includes(endpoint as VideoEndpoint)
    && (VIDEO_ENDPOINT_ADAPTERS[endpoint as VideoEndpoint] as readonly unknown[]).includes(adapter)
}

const present = (value: unknown) => value !== null && value !== undefined && value !== ''

// 按生效方案校验操作路径，模型覆盖和未绑定路径必须遵守同一份合同。
function klingAdapterPathError(path: string, adapter: VideoProtocolAdapter): string | null {
  const officialPaths = ['/text-to-video/{model}', '/image-to-video/{model}']
  if (adapter === 'kling_v1' && !officialPaths.includes(path)) return 'adapterKlingPathInvalid'
  if (adapter === 'kemei_v1' && ![...officialPaths, '/omni-video/{model}'].includes(path)) return 'adapterKemeiKlingPathInvalid'
  return null
}

export function validateVideoAccountForm(form: VideoAccountForm, baseUrl: string): string | null {
  if (!form.endpoints.length) return 'endpointsRequired'
  if (!VIDEO_OPENAI_PROFILES.includes(form.openAIProfile)) return 'openAIProfileInvalid'
  if (form.invalidAdapterConfig || Object.entries(form.endpointAdapters).some(([endpoint, adapter]) => !isVideoEndpointAdapter(endpoint, adapter))) return 'adapterInvalid'
  // Vidu 没有缺省旧协议合同；即使模型有覆盖，也必须明确选择端点方案。
  if (form.endpoints.includes('vidu') && !form.endpointAdapters.vidu) return 'adapterInvalid'
  if (form.endpoints.some(endpoint => !isVideoEndpointAdapter(endpoint, videoEndpointAdapter(form, endpoint)))) return 'adapterInvalid'
  if (Object.entries(form.unboundModelAdapters ?? {}).some(([model, adapters]) => !model.trim() || /[*?]/.test(model)
    || Object.entries(adapters).some(([endpoint, adapter]) => !isVideoEndpointAdapter(endpoint, adapter)))) return 'adapterInvalid'
  if (form.endpoints.some(endpoint => !(form.baseUrls[endpoint] || baseUrl).trim())) return 'baseUrlRequired'
  const pending = Number(form.maxPendingTasks)
  if (!Number.isInteger(pending) || pending < 1 || pending > 1000) return 'pendingInvalid'
  if (present(form.maxDurationSeconds) && (!Number.isFinite(Number(form.maxDurationSeconds)) || Number(form.maxDurationSeconds) <= 0)) return 'budgetInvalid'
  const seen = new Set<string>()
  for (const binding of form.bindings) {
    const model = binding.model.trim()
    if (!model) return 'bindingModelRequired'
    const key = JSON.stringify([model, binding.endpoint])
    if (seen.has(key)) return 'bindingDuplicate'
    if (!form.endpoints.includes(binding.endpoint)) return 'bindingEndpointDisabled'
    if (binding.adapter && /[*?]/.test(model)) return 'adapterModelExactRequired'
    if (binding.adapter && !isVideoEndpointAdapter(binding.endpoint, binding.adapter)) return 'adapterInvalid'
    const allowedPaths: readonly string[] = binding.endpoint === 'kling' ? VIDEO_MODEL_PATHS : binding.endpoint === 'vidu' ? VIDEO_VIDU_PATHS : []
    if (binding.path && !allowedPaths.includes(binding.path)) return 'bindingPathInvalid'
    if (binding.endpoint === 'kling' && binding.path) {
      const pathError = klingAdapterPathError(binding.path, binding.adapter || videoEndpointAdapter(form, 'kling'))
      if (pathError) return pathError
    }
    seen.add(key)
  }
  // 未显式绑定模型的历史路径同样参与校验，适配配置不能悄悄改变旧路径含义。
  for (const [model, path] of Object.entries(form.unboundModelPaths ?? {})) {
    const pathError = klingAdapterPathError(path, form.unboundModelAdapters?.[model]?.kling || videoEndpointAdapter(form, 'kling'))
    if (pathError) return pathError
  }
  if (Object.entries(form.unboundViduModelPaths ?? {}).some(([model, path]) => !model.trim() || !(VIDEO_VIDU_PATHS as readonly string[]).includes(path))) return 'bindingPathInvalid'
  return null
}

// 全量写回视频子配置，清空的预算和映射不从旧凭据偷偷恢复；API Key 由主表单单独保留。
export function videoAccountCredentials(form: VideoAccountForm): Record<string, unknown> {
  // 同一模型聚合多个端点，保留单端点字符串兼容性，不以最后一行覆盖前面的端点。
  const modelEndpoints = new Map<string, VideoEndpoint[]>()
  const modelPaths = { ...form.unboundModelPaths }
  const viduModelPaths = { ...form.unboundViduModelPaths }
  const modelAdapters = new Map(Object.entries(form.unboundModelAdapters ?? {}).map(([model, adapters]) => [model, new Map(Object.entries(adapters))]))
  for (const binding of form.bindings) {
    const model = binding.model.trim()
    const endpoints = modelEndpoints.get(model) || []
    if (!endpoints.includes(binding.endpoint)) endpoints.push(binding.endpoint)
    modelEndpoints.set(model, endpoints)
    // 继承不写覆盖；同模型不同端点各自保存，不让后一行覆盖整组配置。
    const adapters = modelAdapters.get(model) ?? new Map<string, VideoProtocolAdapter>()
    if (binding.adapter) adapters.set(binding.endpoint, binding.adapter)
    else adapters.delete(binding.endpoint)
    modelAdapters.set(model, adapters)
    // 仅 Kling 行有权修改该模型路径，后续其他端点行不会覆盖它。
    if (binding.endpoint === 'kling') {
      if (binding.path) modelPaths[model] = binding.path
      else delete modelPaths[model]
    }
    if (binding.endpoint === 'vidu') {
      if (binding.path) viduModelPaths[model] = binding.path
      else delete viduModelPaths[model]
    }
  }
  return {
    video_endpoints: [...form.endpoints],
    // 取消端点只暂停适配，保留选择以便重新启用；其他端点不会使用该配置。
    video_openai_profile: videoEndpointAdapter(form, 'openai_videos') === 'metaso_h3_v1' ? 'metaso_h3' : 'passthrough',
    video_endpoint_adapters: { ...form.endpointAdapters },
    video_model_adapters: Object.fromEntries([...modelAdapters].filter(([, adapters]) => adapters.size).map(([model, adapters]) => [model, Object.fromEntries(adapters)])),
    video_base_urls: Object.fromEntries(form.endpoints.filter(endpoint => form.baseUrls[endpoint]?.trim()).map(endpoint => [endpoint, form.baseUrls[endpoint]!.trim()])),
    video_model_bindings: Object.fromEntries([...modelEndpoints].map(([model, endpoints]) => [model, endpoints.length === 1 ? endpoints[0] : endpoints])),
    video_model_paths: modelPaths,
    video_vidu_model_paths: viduModelPaths,
    video_max_pending_tasks: Number(form.maxPendingTasks),
    video_max_duration_seconds: present(form.maxDurationSeconds) ? Number(form.maxDurationSeconds) : null,
  }
}
