import type { VideoProtocolProfile } from '@/api/admin/videoProtocolProfiles'

/** 同一键名下的全部版本，版本号倒序，并标出当前启用版本。 */
export interface VideoProtocolProfileGroup {
  key: string
  versions: VideoProtocolProfile[]
  active?: VideoProtocolProfile
}

// 按键名分组，键名升序、版本倒序，便于先看到最新版本。
export function groupVideoProtocolProfiles(profiles: VideoProtocolProfile[]): VideoProtocolProfileGroup[] {
  const groups = new Map<string, VideoProtocolProfile[]>()
  for (const profile of profiles) {
    const list = groups.get(profile.profile_key) ?? []
    list.push(profile)
    groups.set(profile.profile_key, list)
  }
  return [...groups.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([key, versions]) => {
      const sorted = [...versions].sort((a, b) => b.version - a.version)
      return { key, versions: sorted, active: sorted.find(item => item.status === 'active') }
    })
}

// 解析编辑器中的 JSON 文本，只接受对象；返回 null 表示格式无效。
export function parseJsonObject(text: string): Record<string, unknown> | null {
  try {
    const value: unknown = JSON.parse(text)
    return value !== null && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : null
  } catch {
    return null
  }
}

// 可选 JSON 输入：空白返回 undefined，非法返回 null。
export function parseOptionalJson(text: string): unknown {
  if (!text.trim()) return undefined
  try {
    return JSON.parse(text)
  } catch {
    return null
  }
}

export const formatJson = (value: unknown) => JSON.stringify(value, null, 2)

/**
 * 示例模板：创建 POST /v1/video/generations，查询 GET /v1/tasks/{task_id}，成品为直链。
 * 管理员可在此基础上调整字段映射与状态表。
 */
export const VIDEO_PROFILE_STARTER_SPEC = {
  auth: { scheme: 'bearer' },
  capabilities: {
    durations: [5, 10],
    resolutions: ['720p', '1080p'],
    aspect_ratios: ['16:9', '9:16', '1:1'],
    max_images: 1,
    image_roles: ['first_frame'],
  },
  create: {
    path: '/v1/video/generations',
    defaults: { duration: 5, resolution: '720p', aspect_ratio: '16:9' },
    body: [
      { from: 'model', to: 'model', required: true },
      { from: 'prompt', to: 'prompt', required: true },
      { from: 'duration', to: 'duration', type: 'int' },
      { from: 'resolution', to: 'resolution' },
      { from: 'aspect_ratio', to: 'aspect_ratio' },
      { from: 'images[role=first_frame]', to: 'image' },
    ],
    response: { task_id: ['task_id', 'data.task_id', 'id'] },
  },
  poll: {
    path: '/v1/tasks/{task_id}',
    interval_seconds: 10,
    max_processing_minutes: 60,
    response: {
      root: 'data',
      status: 'status',
      status_map: {
        queued: 'queued',
        pending: 'queued',
        processing: 'processing',
        running: 'processing',
        succeeded: 'completed',
        success: 'completed',
        failed: 'failed',
        failure: 'failed',
        cancelled: 'cancelled',
      },
      progress: 'progress',
      video_url: ['video_url', 'output.video_url'],
      error_message: ['fail_reason', 'error.message'],
      seconds: ['duration'],
      resolution: ['resolution'],
    },
  },
  content: { mode: 'url' },
}

// 调试器默认样例：规范请求、创建响应与查询响应，与示例模板相互匹配。
export const VIDEO_PROFILE_SAMPLE_REQUEST = {
  model: 'vendor-video-model',
  prompt: 'A cat walking on the beach at sunset',
  duration: 5,
  resolution: '720p',
  aspect_ratio: '16:9',
  images: [{ url: 'https://example.com/first-frame.png', role: 'first_frame' }],
}

type SpecRecord = Record<string, unknown>
const asRecord = (value: unknown): SpecRecord => (value && typeof value === 'object' && !Array.isArray(value) ? value as SpecRecord : {})
const asArray = (value: unknown): unknown[] => (Array.isArray(value) ? value : [])

/**
 * 按模板自身的映射规则与能力声明生成调试样例，避免固定样例因角色、数量、时长或档位不被模板支持而在映射前失败。
 * 只填写模板实际读取的规范字段（未映射的字段会被服务端拒绝），取值落在 capabilities 与 map 允许的范围内。
 */
export function videoProfileSampleRequest(spec: unknown): Record<string, unknown> {
  const root = asRecord(spec)
  const create = asRecord(root.create)
  const caps = asRecord(root.capabilities)
  const defaults = asRecord(create.defaults)
  const rules = asArray(create.body).map(asRecord)
  // 模板读取的规范字段，以及各字段在 map 中允许的取值。
  const reads = new Set<string>()
  const mapKeys: Record<string, string[]> = {}
  const roleNames = new Set<string>()
  for (const rule of rules) {
    const from = typeof rule.from === 'string' ? rule.from : ''
    if (from) {
      reads.add(from.replace(/\[.*$/, ''))
      const roleSelector = /\[role=([a-z_]+)\]/.exec(from)
      if (roleSelector) roleNames.add(roleSelector[1]!)
      if (rule.map && typeof rule.map === 'object') mapKeys[from] = Object.keys(rule.map as object)
      Object.keys(asRecord(rule.roles)).forEach(role => roleNames.add(role))
    }
    for (const field of asArray(rule.from_all)) if (typeof field === 'string') reads.add(field)
    if (Array.isArray(rule.from_all) && rule.map && typeof rule.map === 'object') {
      // 联合查表的键为各字段取值用 | 连接，取第一组可用组合。
      const first = Object.keys(rule.map as object)[0]?.split('|') ?? []
      rule.from_all.forEach((field, index) => { if (typeof field === 'string' && first[index]) mapKeys[field] = [first[index]!] })
    }
  }
  const pick = (field: string, capability: unknown, fallback: unknown) => {
    const allowed = mapKeys[field]
    const listed = asArray(capability)
    if (allowed?.length) return listed.find(value => allowed.includes(String(value))) ?? allowed[0]
    return listed[0] ?? fallback
  }
  const request: Record<string, unknown> = { model: 'vendor-video-model' }
  if (reads.has('prompt')) request.prompt = 'A cat walking on the beach at sunset'
  if (reads.has('duration')) {
    let duration = Number(pick('duration', caps.durations, defaults.duration ?? 5))
    const min = Number(caps.min_duration) || 0
    const max = Number(caps.max_duration) || 0
    if (min && duration < min) duration = min
    if (max && duration > max) duration = max
    if (caps.integer_duration) duration = Math.round(duration)
    request.duration = duration
  }
  if (reads.has('resolution')) request.resolution = pick('resolution', caps.resolutions, defaults.resolution ?? '720p')
  if (reads.has('aspect_ratio')) request.aspect_ratio = pick('aspect_ratio', caps.aspect_ratios, defaults.aspect_ratio ?? '16:9')
  if (reads.has('images')) {
    const maxImages = Number(caps.max_images) || 1
    const count = Math.min(Math.max(Number(caps.min_images) || 1, 1), maxImages)
    // 角色优先取能力声明，其次取规则中出现的角色；都没有时不写角色，交给 image_role_default。
    const allowedRoles = asArray(caps.image_roles).map(String)
    const roles = allowedRoles.length ? allowedRoles : [...roleNames]
    const ordered = ['first_frame', 'last_frame', 'reference_image'].filter(role => !roles.length || roles.includes(role))
    request.images = Array.from({ length: count }, (_, index) => {
      const role = count === 2 && ordered.includes('first_frame') && ordered.includes('last_frame')
        ? (index === 0 ? 'first_frame' : 'last_frame')
        : (count > 2 && ordered.includes('reference_image') ? 'reference_image' : ordered[0])
      return role ? { url: `https://example.com/reference-${index + 1}.png`, role } : `https://example.com/reference-${index + 1}.png`
    })
  }
  return request
}
export const VIDEO_PROFILE_SAMPLE_CREATE_RESPONSE = { task_id: 'task_123', status: 'queued' }
export const VIDEO_PROFILE_SAMPLE_POLL_RESPONSE = {
  data: { task_id: 'task_123', status: 'succeeded', progress: 100, video_url: 'https://cdn.example.com/task_123.mp4', duration: 5, resolution: '720p' },
}
