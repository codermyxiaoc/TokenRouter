// 内置视频端点；账号不再勾选端点，只在“模型 → 协议”绑定中以 endpoint.adapter 形式出现。
export const VIDEO_ENDPOINTS = ['compat', 'openai_videos', 'seedance', 'kling', 'wan', 'minimax', 'vidu'] as const
export type VideoEndpoint = typeof VIDEO_ENDPOINTS[number]
// 内置协议只保留各家官方格式；厂商与中转格式（Metaso H3、Yezonghui、可美、通用扁平格式）改为预置模板 profile:<key>。
export const VIDEO_ENDPOINT_ADAPTERS = {
  compat: ['openai_v1'],
  openai_videos: ['openai_v1'],
  seedance: ['seedance_v1'],
  kling: ['kling_v1'],
  wan: ['wan_v1'],
  minimax: ['minimax_v2'],
  vidu: ['vidu_v2'],
} as const satisfies Record<VideoEndpoint, readonly string[]>
export type VideoProtocolAdapter = typeof VIDEO_ENDPOINT_ADAPTERS[VideoEndpoint][number]
export const VIDEO_DEFAULT_ADAPTERS: Record<VideoEndpoint, VideoProtocolAdapter> = {
  compat: 'openai_v1', openai_videos: 'openai_v1', seedance: 'seedance_v1',
  kling: 'kling_v1', wan: 'wan_v1', minimax: 'minimax_v2', vidu: 'vidu_v2',
}
// 已由预置模板取代的旧方案，只用于识别并改绑已保存的账号，不能再被选择。
export const VIDEO_DEPRECATED_ADAPTERS = ['legacy_v1', 'metaso_h3_v1', 'kemei_v1', 'yezonghui_v1'] as const
// 测试提示沿用的方案行为：官方协议，或预置模板对应的旧方案。
export type VideoAdapterBehaviour = VideoProtocolAdapter | typeof VIDEO_DEPRECATED_ADAPTERS[number]
// Kling 模型操作路径模板；官方 kling_v1 只支持文生与图生。
export const VIDEO_MODEL_PATHS = ['/text-to-video/{model}', '/image-to-video/{model}'] as const
// Vidu 操作路径；Vidu 绑定必须显式选择其中之一。
export const VIDEO_VIDU_PATHS = ['/ent/v2/img2video', '/ent/v2/reference2video', '/ent/v2/text2video', '/ent/v2/start-end2video'] as const
// 账号其余模型的默认协议键，是唯一允许的通配符。
export const VIDEO_PROTOCOL_WILDCARD = '*'
// 模板协议前缀：profile:<key> 或 profile:<key>@<version>。
export const VIDEO_PROFILE_PREFIX = 'profile:'
// 与服务端一致的模板键名规则。
export const VIDEO_PROFILE_KEY_PATTERN = /^[a-z0-9][a-z0-9_-]{0,63}$/
// 服务端迁移后会删除的旧字段，前端保存时不得再发送。
export const VIDEO_LEGACY_CREDENTIAL_KEYS = [
  'video_endpoints', 'video_model_bindings', 'video_base_urls', 'video_model_paths', 'video_vidu_model_paths',
  'video_openai_profile', 'video_endpoint_adapters', 'video_model_adapters',
] as const

/** 一行“模型 → 协议”绑定；path 仅 Kling/Vidu 内置协议使用，baseUrl 为空时使用账号 Base URL。 */
export interface VideoProtocolRow { model: string; protocol: string; path: string; baseUrl: string }
export interface VideoAccountForm {
  rows: VideoProtocolRow[]
  maxPendingTasks: number | string
  maxDurationSeconds: number | string | null
}

/** 解析后的协议：内置协议给出端点与方案，模板协议给出键名与可选版本。 */
export type ParsedVideoProtocol =
  | { kind: 'builtin'; endpoint: VideoEndpoint; adapter: VideoAdapterBehaviour }
  | { kind: 'profile'; key: string; version?: number }

// 与服务端 parseVideoModelProtocol 相同的解析规则，不推断厂商，也不允许跨端点方案。
export function parseVideoProtocol(value: unknown): ParsedVideoProtocol | null {
  const protocol = typeof value === 'string' ? value.trim() : ''
  if (protocol.startsWith(VIDEO_PROFILE_PREFIX)) {
    const rest = protocol.slice(VIDEO_PROFILE_PREFIX.length)
    const at = rest.indexOf('@')
    const key = at >= 0 ? rest.slice(0, at) : rest
    if (!VIDEO_PROFILE_KEY_PATTERN.test(key)) return null
    if (at < 0) return { kind: 'profile', key }
    const versionText = rest.slice(at + 1)
    const version = Number(versionText)
    if (!/^\d+$/.test(versionText) || !Number.isInteger(version) || version <= 0) return null
    return { kind: 'profile', key, version }
  }
  const dot = protocol.indexOf('.')
  if (dot < 0) return null
  const endpoint = protocol.slice(0, dot)
  const adapter = protocol.slice(dot + 1)
  // 已被取代的旧方案仍可解析，用于展示与试跑尚未迁移的账号；选择框不提供，保存校验会拒绝或改绑。
  if (!isVideoEndpointAdapter(endpoint, adapter) && !isDeprecatedVideoEndpointAdapter(endpoint, adapter)) return null
  return { kind: 'builtin', endpoint: endpoint as VideoEndpoint, adapter: adapter as VideoAdapterBehaviour }
}

export function isVideoEndpointAdapter(endpoint: string, adapter: unknown): adapter is VideoProtocolAdapter {
  return VIDEO_ENDPOINTS.includes(endpoint as VideoEndpoint)
    && (VIDEO_ENDPOINT_ADAPTERS[endpoint as VideoEndpoint] as readonly unknown[]).includes(adapter)
}

// 旧方案的端点组合规则与服务端 ValidVideoProtocolAdapter 一致：原样协议适用全部端点，可美不含 openai_videos，H3 与 Yezonghui 仅限 openai_videos。
function isDeprecatedVideoEndpointAdapter(endpoint: string, adapter: string): boolean {
  if (!VIDEO_ENDPOINTS.includes(endpoint as VideoEndpoint)) return false
  if (adapter === 'legacy_v1') return true
  if (adapter === 'kemei_v1') return endpoint !== 'openai_videos'
  return (adapter === 'metaso_h3_v1' || adapter === 'yezonghui_v1') && endpoint === 'openai_videos'
}

// 全部内置协议值，按端点顺序展开，供协议选择框分组展示。
export const VIDEO_BUILTIN_PROTOCOLS: readonly string[] = VIDEO_ENDPOINTS.flatMap(endpoint =>
  VIDEO_ENDPOINT_ADAPTERS[endpoint].map(adapter => `${endpoint}.${adapter}`))

// compat 仅采用 OpenAI 参数格式，不能将兼容路径误称为 OpenAI 官方端点。
export function videoAdapterTextKey(endpoint: VideoEndpoint | '', adapter: VideoAdapterBehaviour): string {
  return endpoint === 'compat' && adapter === 'openai_v1' ? 'compat_openai' : adapter
}

// 返回协议允许的操作路径；空数组表示该协议不得配置路径。
export function videoProtocolPaths(protocol: string): readonly string[] {
  const parsed = parseVideoProtocol(protocol)
  if (parsed?.kind !== 'builtin') return []
  if (parsed.endpoint === 'vidu') return VIDEO_VIDU_PATHS
  if (parsed.endpoint !== 'kling') return []
  return VIDEO_MODEL_PATHS
}

// Kling/Vidu 协议必须按模型配置操作路径，其他协议禁止配置路径。
export function videoProtocolRequiresPath(protocol: string): boolean {
  const parsed = parseVideoProtocol(protocol)
  return parsed?.kind === 'builtin' && (parsed.endpoint === 'kling' || parsed.endpoint === 'vidu')
}

const objectRecord = (value: unknown): Record<string, unknown> | undefined =>
  value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : undefined
const text = (value: unknown) => typeof value === 'string' ? value.trim() : ''
const present = (value: unknown) => value !== null && value !== undefined && value !== ''

// 把服务端保存的 video_model_protocols 展开成表格行，通配默认值排在最后便于阅读。
function rowsFromProtocols(protocols: Record<string, unknown>): VideoProtocolRow[] {
  return Object.entries(protocols)
    .map(([model, value]) => {
      const binding = objectRecord(value) ?? {}
      return replaceDeprecatedVideoProtocol({ model, protocol: text(binding.protocol), path: text(binding.path), baseUrl: text(binding.base_url) })
    })
    .sort((a, b) => Number(a.model === VIDEO_PROTOCOL_WILDCARD) - Number(b.model === VIDEO_PROTOCOL_WILDCARD))
}

/**
 * 把旧端点配置换算为模型协议绑定，规则与服务端 convertLegacyVideoConfiguration 保持一致：
 * 多端点模型优先 openai_videos，其次 compat，再其次配置顺序中的第一个端点。
 */
export function legacyVideoProtocolRows(credentials: Record<string, unknown>): VideoProtocolRow[] {
  const endpointList = (value: unknown): VideoEndpoint[] =>
    (Array.isArray(value) ? value : [value]).filter((item): item is VideoEndpoint => VIDEO_ENDPOINTS.includes(item as VideoEndpoint))
  const endpoints = endpointList(credentials.video_endpoints ?? [])
  const bindings = objectRecord(credentials.video_model_bindings) ?? {}
  const baseUrls = objectRecord(credentials.video_base_urls) ?? {}
  const modelPaths = objectRecord(credentials.video_model_paths) ?? {}
  const viduPaths = objectRecord(credentials.video_vidu_model_paths) ?? {}
  const endpointAdapters = objectRecord(credentials.video_endpoint_adapters) ?? {}
  const modelAdapters = objectRecord(credentials.video_model_adapters) ?? {}
  const pick = (list: VideoEndpoint[]): VideoEndpoint | undefined =>
    list.find(item => item === 'openai_videos') ?? list.find(item => item === 'compat') ?? list[0]
  // 方案解析顺序：模型覆盖、端点默认、旧 OpenAI profile、原有协议。
  const adapterFor = (model: string, endpoint: VideoEndpoint): unknown =>
    objectRecord(modelAdapters[model])?.[endpoint]
      ?? endpointAdapters[endpoint]
      ?? (endpoint === 'openai_videos' && credentials.video_openai_profile === 'metaso_h3' ? 'metaso_h3_v1' : 'legacy_v1')
  const build = (model: string, endpoint: VideoEndpoint): VideoProtocolRow | null => {
    // 旧厂商方案先按服务端规则改绑到预置模板或官方协议。
    const row = replaceDeprecatedVideoProtocol({
      model, protocol: `${endpoint}.${String(adapterFor(model, endpoint))}`,
      path: endpoint === 'kling' ? text(modelPaths[model]) : endpoint === 'vidu' ? text(viduPaths[model]) : '',
      baseUrl: text(baseUrls[endpoint]),
    })
    // 无法通过新规则校验的旧绑定直接丢弃，与服务端迁移一致；没有等价模板的旧方案保留，提示管理员手动改绑。
    const error = videoProtocolRowError(row)
    return error && error !== 'protocolDeprecated' ? null : row
  }
  const out = new Map<string, VideoProtocolRow>()
  for (const [model, value] of Object.entries(bindings)) {
    const endpoint = pick(endpointList(value))
    const row = endpoint ? build(model, endpoint) : null
    if (row) out.set(model, row)
  }
  const fallback = pick(endpoints)
  if (!fallback) return [...out.values()]
  // 仅有模型级方案覆盖或操作路径、没有显式绑定的模型也生成精确行，不能被默认协议吞掉。
  const overridden = new Map<string, boolean>()
  for (const model of Object.keys(modelAdapters)) overridden.set(model, true)
  for (const model of Object.keys(modelPaths)) overridden.set(model, fallback === 'kling' || overridden.get(model) === true)
  for (const model of Object.keys(viduPaths)) overridden.set(model, fallback === 'vidu' || overridden.get(model) === true)
  for (const [model, needed] of overridden) {
    if (out.has(model) || !needed || !model) continue
    const row = build(model, fallback)
    if (row) out.set(model, row)
  }
  // Kling/Vidu 路径按模型保存，默认绑定无法表达，只为配置过路径的模型生成精确行。
  if (fallback !== 'kling' && fallback !== 'vidu') {
    const row = build(VIDEO_PROTOCOL_WILDCARD, fallback)
    if (row) out.set(VIDEO_PROTOCOL_WILDCARD, row)
  }
  return rowsFromProtocols(Object.fromEntries([...out].map(([model, row]) => [model, {
    protocol: row.protocol, path: row.path, base_url: row.baseUrl,
  }])))
}

// @project-doc docs/interfaces/upstream_account_matrix.md#video_account
// 创建和编辑共用凭据契约；新建表单只给出一行待选择的 * 默认协议，不预设任何供应商。
export function createVideoAccountForm(savedCredentials?: Record<string, unknown>): VideoAccountForm {
  const credentials = savedCredentials ?? {}
  const saved = objectRecord(credentials.video_model_protocols)
  let rows: VideoProtocolRow[]
  if (saved) rows = rowsFromProtocols(saved)
  else if (savedCredentials && VIDEO_LEGACY_CREDENTIAL_KEYS.some(key => key in credentials)) rows = legacyVideoProtocolRows(credentials)
  else rows = []
  if (!rows.length) rows = [emptyVideoProtocolRow(VIDEO_PROTOCOL_WILDCARD)]
  return {
    rows,
    maxPendingTasks: Number(credentials.video_max_pending_tasks ?? 10),
    maxDurationSeconds: credentials.video_max_duration_seconds == null ? null : Number(credentials.video_max_duration_seconds),
  }
}

export function emptyVideoProtocolRow(model = ''): VideoProtocolRow {
  return { model, protocol: '', path: '', baseUrl: '' }
}

// 判断 Base URL 是否为不含凭据、查询串和片段的 HTTP(S) 地址，与服务端校验一致。
export function isValidVideoBaseUrl(value: string): boolean {
  try {
    const url = new URL(value.trim())
    return (url.protocol === 'http:' || url.protocol === 'https:') && !!url.host
      && !url.username && !url.password && !url.search && !url.hash
  } catch {
    return false
  }
}

// 单行协议与路径校验；不检查模型名和地址，供旧配置换算与表单校验复用。
export function videoProtocolRowError(row: VideoProtocolRow): string | null {
  const protocol = row.protocol.trim()
  if (!protocol) return 'protocolRequired'
  // 没有等价预置模板的旧方案（如原样协议的旧版 Kling 路径）必须由管理员手动改绑。
  if (isDeprecatedVideoProtocol(protocol)) return 'protocolDeprecated'
  const parsed = parseVideoProtocol(protocol)
  if (!parsed) return 'protocolInvalid'
  const path = row.path.trim()
  if (!videoProtocolRequiresPath(protocol)) return path ? 'protocolPathForbidden' : null
  if (!path) return 'protocolPathRequired'
  if (parsed.kind === 'builtin' && parsed.endpoint === 'kling') {
    return videoProtocolPaths(protocol).includes(path) ? null : 'adapterKlingPathInvalid'
  }
  return videoProtocolPaths(protocol).includes(path) ? null : 'bindingPathInvalid'
}

// 返回 i18n 键（admin.accounts.video.<key>）；null 表示可以保存。
export function validateVideoAccountForm(form: VideoAccountForm, baseUrl: string): string | null {
  if (!form.rows.length) return 'protocolsRequired'
  const seen = new Set<string>()
  for (const row of form.rows) {
    const model = row.model.trim()
    if (!model) return 'bindingModelRequired'
    if (model !== VIDEO_PROTOCOL_WILDCARD && /[*?]/.test(model)) return 'protocolModelWildcardInvalid'
    if (seen.has(model)) return 'bindingDuplicate'
    seen.add(model)
    const error = videoProtocolRowError(row)
    if (error) return error
    const base = row.baseUrl.trim() || baseUrl.trim()
    if (!base) return 'baseUrlRequired'
    if (!isValidVideoBaseUrl(base)) return 'baseUrlInvalid'
  }
  const pending = Number(form.maxPendingTasks)
  if (!Number.isInteger(pending) || pending < 1 || pending > 1000) return 'pendingInvalid'
  if (present(form.maxDurationSeconds) && (!Number.isFinite(Number(form.maxDurationSeconds)) || Number(form.maxDurationSeconds) <= 0)) return 'budgetInvalid'
  return null
}

// 全量写回视频子配置；只发送 video_model_protocols 与预算字段，旧字段由调用方清理。
export function videoAccountCredentials(form: VideoAccountForm): Record<string, unknown> {
  const protocols: Record<string, Record<string, string>> = {}
  for (const row of form.rows) {
    const binding: Record<string, string> = { protocol: row.protocol.trim() }
    if (videoProtocolRequiresPath(binding.protocol) && row.path.trim()) binding.path = row.path.trim()
    if (row.baseUrl.trim()) binding.base_url = row.baseUrl.trim()
    protocols[row.model.trim()] = binding
  }
  return {
    video_model_protocols: protocols,
    video_max_pending_tasks: Number(form.maxPendingTasks),
    video_max_duration_seconds: present(form.maxDurationSeconds) ? Number(form.maxDurationSeconds) : null,
  }
}

// 编辑保存时从原凭据中删除旧字段，避免把已迁移的端点配置再写回服务端。
export function stripLegacyVideoCredentials(credentials: Record<string, unknown>): Record<string, unknown> {
  for (const key of VIDEO_LEGACY_CREDENTIAL_KEYS) delete credentials[key]
  return credentials
}

// 判断协议是否为已被取代的旧厂商方案（endpoint.legacy_v1 等）。
export function isDeprecatedVideoProtocol(protocol: string): boolean {
  const dot = protocol.indexOf('.')
  return dot > 0 && isDeprecatedVideoEndpointAdapter(protocol.slice(0, dot), protocol.slice(dot + 1))
}

const VIDEO_KEMEI_ENDPOINT_PRESETS: Partial<Record<VideoEndpoint, string>> = {
  compat: 'kemei-compat', seedance: 'kemei-seedance', minimax: 'kemei-minimax', wan: 'kemei-wan',
}
const VIDEO_KEMEI_KLING_PRESETS: Record<string, string> = {
  '/text-to-video/{model}': 'kemei-kling-text', '/image-to-video/{model}': 'kemei-kling-image', '/omni-video/{model}': 'kemei-kling-omni',
}
const VIDEO_LEGACY_OFFICIAL: Partial<Record<VideoEndpoint, VideoProtocolAdapter>> = {
  seedance: 'seedance_v1', minimax: 'minimax_v2', wan: 'wan_v1', vidu: 'vidu_v2',
}

/**
 * 与服务端 videoDeprecatedProtocolReplacement 一致：厂商方案改绑到预置模板，原样协议在原生端点改为官方协议。
 * 没有等价替代（如原样协议的旧版 Kling 路径）时原样返回，由校验提示手动改绑。
 */
export function replaceDeprecatedVideoProtocol(row: VideoProtocolRow): VideoProtocolRow {
  const protocol = row.protocol.trim()
  if (!isDeprecatedVideoProtocol(protocol)) return row
  const dot = protocol.indexOf('.')
  const endpoint = protocol.slice(0, dot) as VideoEndpoint
  const adapter = protocol.slice(dot + 1)
  const path = row.path.trim()
  const profile = (key: string | undefined): VideoProtocolRow => key ? { ...row, protocol: VIDEO_PROFILE_PREFIX + key, path: '' } : row
  switch (adapter) {
    case 'metaso_h3_v1':
      return profile(endpoint === 'openai_videos' ? 'metaso-h3' : undefined)
    case 'yezonghui_v1':
      return profile(endpoint === 'openai_videos' ? 'yezonghui' : undefined)
    case 'kemei_v1':
      if (endpoint === 'kling') return profile(VIDEO_KEMEI_KLING_PRESETS[path])
      if (endpoint === 'vidu') return profile((VIDEO_VIDU_PATHS as readonly string[]).includes(path) ? `kemei-vidu-${path.slice('/ent/v2/'.length)}` : undefined)
      return profile(VIDEO_KEMEI_ENDPOINT_PRESETS[endpoint])
    case 'legacy_v1': {
      if (endpoint === 'compat') return profile('generic-flat')
      if (endpoint === 'openai_videos') return profile('generic-flat-videos')
      const official = endpoint === 'kling'
        ? ((VIDEO_MODEL_PATHS as readonly string[]).includes(path) ? 'kling_v1' : undefined)
        : VIDEO_LEGACY_OFFICIAL[endpoint]
      return official ? { ...row, protocol: `${endpoint}.${official}` } : row
    }
  }
  return row
}

/**
 * 预置模板沿用原厂商方案的测试提示（档位、比例默认值）；其它模板返回 null，按通用参数处理。
 * 可美模板的端点从键名推断，仅用于提示，不参与路由。
 */
export function videoPresetBehaviour(parsed: ParsedVideoProtocol | null): { adapter: VideoAdapterBehaviour; endpoint: VideoEndpoint | '' } | null {
  if (parsed?.kind !== 'profile') return null
  const key = parsed.key
  if (key === 'metaso-h3') return { adapter: 'metaso_h3_v1', endpoint: 'openai_videos' }
  if (key === 'yezonghui') return { adapter: 'yezonghui_v1', endpoint: 'openai_videos' }
  if (key === 'generic-flat') return { adapter: 'legacy_v1', endpoint: 'compat' }
  if (key === 'generic-flat-videos') return { adapter: 'legacy_v1', endpoint: 'openai_videos' }
  const kemei = /^kemei-(compat|seedance|minimax|wan|kling|vidu)(?:-|$)/.exec(key)
  return kemei ? { adapter: 'kemei_v1', endpoint: kemei[1] as VideoEndpoint } : null
}
