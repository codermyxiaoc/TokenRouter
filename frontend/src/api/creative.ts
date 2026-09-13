/**
 * Creative Studio（创作台）API
 * 全部走 apiClient（JWT 会话认证），不涉及、不携带任何 API Key。
 */

import { apiClient } from './client'

// ==================== 类型定义 ====================

// 创作台支持的操作类型，以服务端返回的 operations 为准
export type CreativeOperation = 'generate' | 'edit' | 'inpaint' | (string & {})

// 服务端能力协议中的数值范围；当前创作台固定 PNG，不会下发压缩范围。
export interface CreativeNumericRange {
  min: number
  max: number
  step: number
}

// 模型目录项：分组 + 模型的合成选项
export interface CreativeModelOption {
  group_id: string
  group_name: string
  model: string
  operations: CreativeOperation[]
  image_sizes: string[]
  aspect_ratios: string[]
  qualities: string[]
  output_formats: string[]
  output_compression?: CreativeNumericRange | null
  background_options?: string[]
  thinking_levels?: string[]
  max_output_count?: number
  max_reference_images?: number
  // 图片展示单价，直接使用服务端统一定价结果；512 仅供支持该档位的 Gemini 模型使用
  price_512?: number
  price_1k: number
  price_2k: number
  price_4k: number
}

// run 终态集合，轮询遇到这些状态即停止
export const CREATIVE_RUN_TERMINAL_STATUSES: readonly string[] = [
  'succeeded',
  'failed',
  'cancelled',
  'result_lost',
]

export type CreativeRunStatus =
  | 'queued'
  | 'running'
  | 'provider_succeeded'
  | 'settlement_pending'
  | 'release_pending'
  | 'succeeded'
  | 'failed'
  | 'cancelled'
  | 'result_lost'
  | (string & {})

export type CreativeOutputStatus = 'succeeded' | 'failed' | 'cancelled' | 'acked' | (string & {})

// 单个输出位的元信息（图片本体不落库，transient 存储）
export interface CreativeRunOutput {
  output_index: number
  status: CreativeOutputStatus
  mime_type?: string
  byte_size?: number
  transient_expires_at?: number | null
  acked_at?: number | null
  error_code?: string
  error_message?: string
}

export interface CreativeRun {
  id: string
  status: CreativeRunStatus
  operation: CreativeOperation
  model: string
  group_id: string
  requested_output_count: number
  output_format?: string
  estimated_cost?: number
  hold_amount?: number
  actual_cost?: number | null
  error_code?: string
  error_message?: string
  created_at?: number
  started_at?: number | null
  completed_at?: number | null
  cancelled_at?: number | null
  outputs?: CreativeRunOutput[]
}

// 列表接口分页结构（兼容直接返回数组的宽松处理在调用方完成）
export interface CreativeRunsPage {
  items: CreativeRun[]
  total: number
}

export interface CreativeActiveRunsPage {
  items: CreativeRun[]
  next_cursor: string
  has_more: boolean
}

export interface CreativeCapabilities {
  max_prompt_chars: number
  max_asset_bytes: number
  max_total_input_bytes: number
  max_mask_bytes: number
  allowed_mime_types: string[]
}

// ==================== API 方法 ====================

/**
 * 获取创作台可用模型目录（分组 + 模型 + 支持操作 + 支持尺寸）
 */
export async function getCreativeModels(): Promise<CreativeModelOption[]> {
  const { data } = await apiClient.get<CreativeModelOption[]>('/creative/models')
  return Array.isArray(data) ? data : []
}

/** 获取服务端输入限制，前端校验只使用该契约。 */
export async function getCreativeCapabilities(): Promise<CreativeCapabilities> {
  const { data } = await apiClient.get<CreativeCapabilities>('/creative/capabilities')
  return {
    max_prompt_chars: Number(data?.max_prompt_chars) || 8000,
    max_asset_bytes: Number(data?.max_asset_bytes) || 33554432,
    max_total_input_bytes: Number(data?.max_total_input_bytes) || 67108864,
    max_mask_bytes: Number(data?.max_mask_bytes) || 4194304,
    allowed_mime_types: Array.isArray(data?.allowed_mime_types) ? data.allowed_mime_types : ['image/png', 'image/jpeg', 'image/webp'],
  }
}

/**
 * 创建创作 run（multipart/form-data）
 * @param form 已组好的表单（源图、mask、prompt 等）
 * @param idempotencyKey 可选幂等键：同一表单重试时复用同一 key
 */
export async function createCreativeRun(
  form: FormData,
  workspaceId: string,
  idempotencyKey?: string,
): Promise<CreativeRun> {
  const { data } = await apiClient.post<CreativeRun>('/creative/runs', form, {
    headers: {
      // 必须覆盖实例默认的 application/json，否则 transformRequest 会把 FormData 序列化成 JSON
      'Content-Type': 'multipart/form-data',
      'X-Creative-Workspace-ID': workspaceId,
      ...(idempotencyKey ? { 'Idempotency-Key': idempotencyKey } : {}),
    },
  })
  return data
}

/**
 * 查询 run 列表。服务端可能返回 {items,total} 也可能直接返回数组，这里宽松归一化。
 */
export async function getCreativeRuns(workspaceId: string, _page = 1, pageSize = 20): Promise<CreativeRunsPage> {
  const { data } = await apiClient.get<CreativeRunsPage | CreativeRun[]>('/creative/runs', {
    // 后端当前使用 limit，保留 page 形参用于兼容现有调用方，后续扩展 offset 时无需改签名。
    params: { limit: pageSize },
    headers: { 'X-Creative-Workspace-ID': workspaceId },
  })
  if (Array.isArray(data)) {
    return { items: data, total: data.length }
  }
  const items = Array.isArray(data?.items) ? data.items : []
  return { items, total: typeof data?.total === 'number' ? data.total : items.length }
}

/** 查询所有活动任务；cursor 由服务端生成，不能由调用方解释。 */
export async function getCreativeActiveRuns(
  workspaceId: string,
  cursor?: string,
  limit = 100,
): Promise<CreativeActiveRunsPage> {
  const { data } = await apiClient.get<CreativeActiveRunsPage>('/creative/runs/active', {
    params: { limit, ...(cursor ? { cursor } : {}) },
    headers: { 'X-Creative-Workspace-ID': workspaceId },
  })
  return {
    items: Array.isArray(data?.items) ? data.items : [],
    next_cursor: typeof data?.next_cursor === 'string' ? data.next_cursor : '',
    has_more: data?.has_more === true,
  }
}

/**
 * 查询单个 run 详情（含 outputs 元信息）
 */
export async function getCreativeRun(id: string, workspaceId: string): Promise<CreativeRun> {
  const { data } = await apiClient.get<CreativeRun>(`/creative/runs/${encodeURIComponent(id)}`, {
    headers: { 'X-Creative-Workspace-ID': workspaceId },
  })
  return data
}

/**
 * 获取输出图片二进制（transient，取回后应立即本地保存并 ack）。
 * 注意：二进制响应不适用 {code,message,data} envelope。
 */
export async function getCreativeRunOutputContent(runId: string, index: number, workspaceId: string): Promise<Blob> {
  const { data } = await apiClient.get<Blob>(
    `/creative/runs/${encodeURIComponent(runId)}/outputs/${encodeURIComponent(String(index))}/content`,
    { responseType: 'blob', timeout: 300000, headers: { 'X-Creative-Workspace-ID': workspaceId } },
  )
  return data
}

/**
 * 确认输出已持久化到本地（服务端据此清理 transient 资源）
 */
export async function ackCreativeRunOutput(runId: string, index: number, workspaceId: string): Promise<void> {
  await apiClient.post(
    `/creative/runs/${encodeURIComponent(runId)}/outputs/${encodeURIComponent(String(index))}/ack`,
    undefined,
    { headers: { 'X-Creative-Workspace-ID': workspaceId } },
  )
}
