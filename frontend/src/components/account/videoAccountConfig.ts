// 两种 OpenAI 视频路径是独立能力，保留旧 compat 标识，不自动迁移历史账号。
export const VIDEO_ENDPOINTS = ['compat', 'openai_videos', 'seedance', 'kling', 'wan', 'minimax'] as const
export type VideoEndpoint = typeof VIDEO_ENDPOINTS[number]
export const VIDEO_MODEL_PATHS = ['/text-to-video/{model}', '/image-to-video/{model}', '/omni-video/{model}', '/v1/videos/text2video', '/v1/videos/omni-video'] as const

export interface VideoModelBindingForm { model: string; endpoint: VideoEndpoint; path: string }
export interface VideoAccountForm {
  endpoints: VideoEndpoint[]
  baseUrls: Partial<Record<VideoEndpoint, string>>
  bindings: VideoModelBindingForm[]
  // 没有显式 Kling 绑定的历史路径仍可能用于继承端点，不擅自新增绑定收窄能力。
  unboundModelPaths?: Record<string, string>
  maxPendingTasks: number | string
  maxDurationSeconds: number | string | null
}

// @project-doc docs/interfaces/upstream_account_matrix.md#video_account
// 创建和编辑共用凭据契约；仅回填明确保存的端点，不默认启用兼容入口。
export function createVideoAccountForm(credentials: Record<string, unknown> = {}): VideoAccountForm {
  const endpoints = Array.isArray(credentials.video_endpoints)
    ? credentials.video_endpoints.filter((value): value is VideoEndpoint => VIDEO_ENDPOINTS.includes(value as VideoEndpoint))
    : []
  const bindings = (credentials.video_model_bindings || {}) as Record<string, VideoEndpoint | VideoEndpoint[]>
  const paths = (credentials.video_model_paths || {}) as Record<string, string>
  return {
    endpoints,
    baseUrls: { ...(credentials.video_base_urls || {}) as Partial<Record<VideoEndpoint, string>> },
    // 旧字符串和新数组统一展开成行；模型路径只属于 Kling 行，不能被其他协议行覆盖。
    bindings: Object.entries(bindings).flatMap(([model, value]) =>
      (Array.isArray(value) ? value : [value]).map(endpoint => ({ model, endpoint, path: endpoint === 'kling' ? paths[model] || '' : '' }))),
    unboundModelPaths: Object.fromEntries(Object.entries(paths).filter(([model]) => {
      const value = bindings[model]
      return !(Array.isArray(value) ? value : [value]).includes('kling')
    })),
    maxPendingTasks: Number(credentials.video_max_pending_tasks ?? 10),
    maxDurationSeconds: credentials.video_max_duration_seconds == null ? null : Number(credentials.video_max_duration_seconds),
  }
}

const present = (value: unknown) => value !== null && value !== undefined && value !== ''

export function validateVideoAccountForm(form: VideoAccountForm, baseUrl: string): string | null {
  if (!form.endpoints.length) return 'endpointsRequired'
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
    if (binding.path && (binding.endpoint !== 'kling' || !(VIDEO_MODEL_PATHS as readonly string[]).includes(binding.path))) return 'bindingPathInvalid'
    seen.add(key)
  }
  return null
}

// 全量写回视频子配置，清空的预算和映射不从旧凭据偷偷恢复；API Key 由主表单单独保留。
export function videoAccountCredentials(form: VideoAccountForm): Record<string, unknown> {
  // 同一模型聚合多个端点，保留单端点字符串兼容性，不以最后一行覆盖前面的端点。
  const modelEndpoints = new Map<string, VideoEndpoint[]>()
  const modelPaths = { ...form.unboundModelPaths }
  for (const binding of form.bindings) {
    const model = binding.model.trim()
    const endpoints = modelEndpoints.get(model) || []
    if (!endpoints.includes(binding.endpoint)) endpoints.push(binding.endpoint)
    modelEndpoints.set(model, endpoints)
    // 仅 Kling 行有权修改该模型路径，后续其他端点行不会覆盖它。
    if (binding.endpoint === 'kling') {
      if (binding.path) modelPaths[model] = binding.path
      else delete modelPaths[model]
    }
  }
  return {
    video_endpoints: [...form.endpoints],
    video_base_urls: Object.fromEntries(form.endpoints.filter(endpoint => form.baseUrls[endpoint]?.trim()).map(endpoint => [endpoint, form.baseUrls[endpoint]!.trim()])),
    video_model_bindings: Object.fromEntries([...modelEndpoints].map(([model, endpoints]) => [model, endpoints.length === 1 ? endpoints[0] : endpoints])),
    video_model_paths: modelPaths,
    video_max_pending_tasks: Number(form.maxPendingTasks),
    video_max_duration_seconds: present(form.maxDurationSeconds) ? Number(form.maxDurationSeconds) : null,
  }
}
