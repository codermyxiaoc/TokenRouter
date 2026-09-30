import { apiClient } from './client'

export const mediaTaskTypes = ['image', 'video'] as const
export const mediaTaskStatuses = ['queued', 'processing', 'completed', 'failed', 'cancelled', 'expired'] as const
export const mediaTaskSources = ['async_image', 'grok_video', 'seedance_video'] as const

export interface MediaTask {
  id: number
  source: string
  task_id: string
  media_type: string
  platform: string
  model: string
  status: string
  upstream_status: string
  user_id: number
  // 用户身份摘要只由管理员任务记录接口返回。
  user?: { id: number; email: string; username: string; deleted_at?: string | null } | null
  api_key_id: number
  group_id: number | null
  account_id: number | null
  group_name: string | null
  http_status: number
  error_message: string
  request_id: string
  created_at: string
  updated_at: string
  completed_at: string | null
  expires_at: string | null
  actual_cost: string | number | null
  billing_mode: string | null
}

export interface MediaTaskFilters {
  page: number
  page_size: number
  media_type?: string
  status?: string
  source?: string
  model?: string
  model_exact?: boolean
  user_id?: number
}

export interface MediaTaskPage {
  items: MediaTask[]
  total: number
  page: number
  page_size: number
  pages: number
}

// 预览只读取已保存的结果，未知媒体属性保持为空，不用请求参数冒充产物属性。
export interface MediaTaskPreviewItem {
  url: string
  media_type: 'image' | 'video'
  mime_type?: string
  width?: number
  height?: number
  duration_seconds?: number
  size_bytes?: number
}

export interface MediaTaskPreview {
  items: MediaTaskPreviewItem[]
  unavailable_reason?: 'pending' | 'expired' | 'unavailable'
  expires_at?: string
}

export type MediaTaskModelFilters = Omit<MediaTaskFilters, 'page' | 'page_size' | 'model' | 'model_exact'>

// 两个只读入口共用响应契约；用户入口不发送管理员专用的用户筛选字段。
export function mediaTasksAPI(admin = false) {
  const base = admin ? '/admin/media-tasks' : '/media-tasks'
  return {
    async list(filters: MediaTaskFilters): Promise<MediaTaskPage> {
      const { user_id, ...shared } = filters
      const params = admin ? { ...shared, user_id } : shared
      const { data } = await apiClient.get<MediaTaskPage>(base, { params })
      return data
    },
    async get(id: number): Promise<MediaTask> {
      const { data } = await apiClient.get<MediaTask>(`${base}/${id}`)
      return data
    },
    async models(filters: MediaTaskModelFilters = {}): Promise<string[]> {
      const { user_id, ...shared } = filters
      const { data } = await apiClient.get<string[]>(`${base}/models`, {
        params: admin ? { ...shared, user_id } : shared,
      })
      return data
    },
    async preview(id: number): Promise<MediaTaskPreview> {
      const { data } = await apiClient.get<MediaTaskPreview>(`${base}/${id}/preview`)
      return data
    },
  }
}
