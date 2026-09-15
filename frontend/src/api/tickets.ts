import { apiClient } from './client'
import type { BasePaginationResponse } from '@/types'

// 前后端统一使用三类工单，历史分类由服务端迁移为咨询。
export const ticketTypes = ['consultation', 'financial', 'technical'] as const
export const ticketPriorities = ['low', 'normal', 'high'] as const
// 标题上限按 Unicode 码点计算，与服务端 RuneCountInString 保持一致。
export const ticketTitleMaxLength = 50
export const ticketStatuses = ['pending', 'waiting_user', 'completed', 'cancelled', 'expired'] as const
export type TicketType = typeof ticketTypes[number]
export type TicketPriority = typeof ticketPriorities[number]
export type TicketStatus = typeof ticketStatuses[number]
export type TicketClosedByRole = 'user' | 'admin' | 'system' | ''

export interface TicketSettings {
  enabled: boolean
  max_open_tickets: number
  max_attachments: number
  max_attachment_size_mb: number
  notify_on_staff_reply: boolean
  auto_expire_hours: number
}

export interface TicketAttachment {
  id: number
  message_id: number
  filename: string
  content_type: string
  size: number
  created_at: string
}

export interface TicketMessage {
  id: number
  ticket_id: number
  user_id: number
  sender_name: string
  is_staff: boolean
  content: string
  created_at: string
  attachments: TicketAttachment[]
}

export interface Ticket {
  id: number
  user_id: number
  user_email: string
  user_name: string
  title: string
  content: string
  type: TicketType
  priority: TicketPriority
  status: TicketStatus
  order_id?: number | null
  created_at: string
  updated_at: string
  last_staff_reply_at?: string | null
  closed_at?: string | null
  closed_by?: number | null
  closed_by_role?: TicketClosedByRole | null
  messages?: TicketMessage[]
  order?: { id: number; out_trade_no: string; amount: number; pay_amount: number; currency?: string; status: string } | null
}

export interface CreateTicketRequest {
  type: TicketType
  title: string
  content: string
  priority: TicketPriority
  order_id?: number
}

export interface TicketFilters {
  type?: string
  status?: string
  priority?: string
  q?: string
  page?: number
  page_size?: number
}

// 创建和回复共享 multipart 协议；附件下载始终经鉴权客户端，不能将 JWT 拼进地址。
function multipart(payload: CreateTicketRequest | { content: string }, files: File[]): FormData {
  const data = new FormData()
  data.append('payload', JSON.stringify(payload))
  files.forEach(file => data.append('files', file))
  return data
}

export function ticketAPI(admin = false) {
  const base = admin ? '/admin/tickets' : '/tickets'
  return {
    async list(params: TicketFilters) {
      return (await apiClient.get<BasePaginationResponse<Ticket>>(base, { params })).data
    },
    async get(id: number, signal?: AbortSignal) {
      return (await apiClient.get<Ticket>(`${base}/${id}`, { signal })).data
    },
    async create(payload: CreateTicketRequest, files: File[], idempotencyKey: string) {
      return (await apiClient.post<Ticket>('/tickets', multipart(payload, files), {
        headers: { 'Content-Type': 'multipart/form-data', 'Idempotency-Key': idempotencyKey },
      })).data
    },
    async reply(id: number, content: string, files: File[], idempotencyKey: string) {
      return (await apiClient.post<Ticket>(`${base}/${id}/replies`, multipart({ content }, files), {
        headers: { 'Content-Type': 'multipart/form-data', 'Idempotency-Key': idempotencyKey },
      })).data
    },
    async close(id: number, action: 'cancel' | 'complete') {
      return (await apiClient.post<Ticket>(`${base}/${id}/${action}`)).data
    },
    async update(id: number, payload: { priority: TicketPriority }) {
      return (await apiClient.patch<Ticket>(`/admin/tickets/${id}`, payload)).data
    },
    async download(id: number, attachmentId: number) {
      return (await apiClient.get<Blob>(`${base}/${id}/attachments/${attachmentId}`, { responseType: 'blob' })).data
    },
    // 预览仍使用鉴权客户端；保留 401 刷新逻辑，其余 Blob 错误在这里有界解析。
    async preview(id: number, attachmentId: number, signal?: AbortSignal) {
      const response = await apiClient.get<Blob>(`${base}/${id}/attachments/${attachmentId}/preview`, {
        responseType: 'blob', signal, validateStatus: status => status !== 401,
      })
      if (response.status >= 200 && response.status < 300) return response.data
      let details: Record<string, unknown> = {}
      if (response.data.size <= 64 * 1024 && response.data.type.includes('json')) {
        try {
          const parsed: unknown = JSON.parse(await response.data.text())
          if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) details = parsed as Record<string, unknown>
        } catch { /* 非 JSON 错误页使用通用提示，不把 HTML 当正文或错误文案渲染。 */ }
      }
      throw { status: response.status, reason: typeof details.reason === 'string' ? details.reason : undefined, code: details.code }
    },
    async config() {
      return (await apiClient.get<TicketSettings>(admin ? `${base}/settings` : `${base}/config`)).data
    },
  }
}

export async function saveTicketSettings(settings: TicketSettings): Promise<TicketSettings> {
  return (await apiClient.put<TicketSettings>('/admin/tickets/settings', settings)).data
}
