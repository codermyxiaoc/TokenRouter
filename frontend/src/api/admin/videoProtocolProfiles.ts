/**
 * 管理员视频协议模板接口：列出版本、保存新版本、启停版本与离线调试预览。
 */

import { apiClient } from '../client'

export type VideoProtocolProfileStatus = 'draft' | 'active' | 'disabled'

/** 模板规格保持为任意 JSON，由服务端负责完整校验。 */
export type VideoProtocolProfileSpec = Record<string, unknown>

export interface VideoProtocolProfile {
  id: number
  profile_key: string
  version: number
  name: string
  status: VideoProtocolProfileStatus
  spec: VideoProtocolProfileSpec
  spec_hash: string
  created_by?: number
  created_at: string
}

export interface SaveVideoProtocolProfileRequest {
  profile_key: string
  name: string
  spec: VideoProtocolProfileSpec
  activate: boolean
}

export interface VideoProtocolProfilePreviewRequest {
  spec: VideoProtocolProfileSpec
  request: Record<string, unknown>
  model?: string
  create_response?: unknown
  poll_response?: unknown
}

/** 单次响应样例的解析结果。 */
export interface VideoProtocolProfileObserved {
  task_id?: string
  task_refs?: Record<string, string>
  status?: string
  upstream_status?: string
  video_url?: string
  seconds?: number
  tokens?: number | null
  resolution?: string
  matches_task: boolean
  error?: string
}

/** 调试预览结果：只包含模板生成的请求与解析出的规范字段，不访问上游。 */
export interface VideoProtocolProfilePreview {
  create_path: string
  create_body?: unknown
  billing: {
    model?: string
    resolution?: string
    duration_seconds?: number
    size?: string
    has_reference_video?: boolean
    reference_image_count?: number
    tokens?: number | null
  }
  create?: VideoProtocolProfileObserved
  poll_path?: string
  poll?: VideoProtocolProfileObserved
  content_path?: string
  request_error?: string
}

export async function list(): Promise<VideoProtocolProfile[]> {
  const { data } = await apiClient.get<VideoProtocolProfile[]>('/admin/video-protocol-profiles')
  return data ?? []
}

export async function save(payload: SaveVideoProtocolProfileRequest): Promise<VideoProtocolProfile> {
  const { data } = await apiClient.post<VideoProtocolProfile>('/admin/video-protocol-profiles', payload)
  return data
}

export async function setStatus(key: string, version: number, status: VideoProtocolProfileStatus): Promise<void> {
  await apiClient.put(`/admin/video-protocol-profiles/${encodeURIComponent(key)}/versions/${version}/status`, { status })
}

export async function preview(payload: VideoProtocolProfilePreviewRequest): Promise<VideoProtocolProfilePreview> {
  const { data } = await apiClient.post<VideoProtocolProfilePreview>('/admin/video-protocol-profiles/preview', payload)
  return data
}

/** 模板 AI 生成配置；密钥只写不读，留空表示保留原值。 */
export interface VideoProfileAIConfig {
  enabled: boolean
  base_url: string
  model: string
  timeout_seconds: number
  api_key_configured: boolean
}

export interface UpdateVideoProfileAIConfigRequest {
  enabled: boolean
  base_url: string
  model: string
  timeout_seconds: number
  api_key?: string
  clear_api_key?: boolean
}

export interface VideoProfileAIGenerateRequest {
  document: string
  instructions?: string
  base_spec?: VideoProtocolProfileSpec
}

/** AI 生成的模板草稿；valid 为 false 时 validation_error 给出最后一轮的校验错误。 */
export interface VideoProfileAIGenerateResult {
  profile_key: string
  name: string
  spec: VideoProtocolProfileSpec
  notes: string
  valid: boolean
  validation_error?: string
  attempts: number
}

export async function getAIConfig(): Promise<VideoProfileAIConfig> {
  const { data } = await apiClient.get<VideoProfileAIConfig>('/admin/video-protocol-profiles/ai-config')
  return data
}

export async function updateAIConfig(payload: UpdateVideoProfileAIConfigRequest): Promise<VideoProfileAIConfig> {
  const { data } = await apiClient.put<VideoProfileAIConfig>('/admin/video-protocol-profiles/ai-config', payload)
  return data
}

// 生成可能包含多轮模型调用，单独放宽请求超时（服务端每轮最多 10 分钟）。
export async function generateWithAI(payload: VideoProfileAIGenerateRequest): Promise<VideoProfileAIGenerateResult> {
  const { data } = await apiClient.post<VideoProfileAIGenerateResult>('/admin/video-protocol-profiles/ai-generate', payload, { timeout: 30 * 60 * 1000 })
  return data
}

/** 删除单个版本；启用中或被账号锁定的版本由服务端拒绝。 */
export async function removeVersion(key: string, version: number): Promise<void> {
  await apiClient.delete(`/admin/video-protocol-profiles/${encodeURIComponent(key)}/versions/${version}`)
}

/** 删除整个模板的全部版本；仍被视频账号绑定时由服务端拒绝。 */
export async function removeProfile(key: string): Promise<void> {
  await apiClient.delete(`/admin/video-protocol-profiles/${encodeURIComponent(key)}`)
}

export const videoProtocolProfilesAPI = { list, save, setStatus, preview, removeVersion, removeProfile, getAIConfig, updateAIConfig, generateWithAI }

export default videoProtocolProfilesAPI
