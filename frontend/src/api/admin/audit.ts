/**
 * 管理员操作审计日志 API。
 *
 * 审计日志仅管理员可见，记录管理面操作、掩码后的请求头凭证和脱敏后的请求体。
 * 不支持单条删除；全量清空必须现场完成 TOTP 验证。
 */

import { apiClient } from '../client'
import type { PaginatedResponse } from '@/types'

export interface AuditLog {
  id: number
  created_at: string
  actor_user_id?: number
  actor_email: string
  actor_role: string
  auth_method: string
  credential_masked: string
  action: string
  method: string
  path: string
  request_id: string
  client_ip: string
  user_agent: string
  request_body?: string
  status_code: number
  latency_ms: number
  extra?: Record<string, any>
}

export interface AuditLogQuery {
  page?: number
  page_size?: number
  start_time?: string
  end_time?: string
  actor_user_id?: number
  actor_email?: string
  auth_method?: string
  action?: string
  method?: string
  client_ip?: string
  success?: string
  q?: string
}

export type AuditLogListResponse = PaginatedResponse<AuditLog>

/** 分页、按条件查询审计日志。 */
export async function list(params: AuditLogQuery): Promise<AuditLogListResponse> {
  const { data } = await apiClient.get('/admin/audit-logs', { params })
  return data
}

/** 查询单条审计日志，响应包含脱敏后的请求体。 */
export async function get(id: number): Promise<AuditLog> {
  const { data } = await apiClient.get(`/admin/audit-logs/${id}`)
  return data
}

/**
 * 全量清空审计日志，由服务端现场校验 TOTP；操作者未启用二次验证时不可用。
 * @param totpCode 当前六位 TOTP 验证码
 */
export async function clear(totpCode: string): Promise<{ deleted: number }> {
  const { data } = await apiClient.post('/admin/audit-logs/clear', { totp_code: totpCode })
  return data
}

export const auditAPI = {
  list,
  get,
  clear
}

export default auditAPI
