import { apiClient } from '../client'

export type QoderSite = 'global' | 'cn'

export interface QoderAuthUrlResponse {
  auth_url: string
  session_id: string
  state: string
  expires_in?: number
  interval?: number
  site?: QoderSite
}

export interface QoderAuthUrlRequest {
  proxy_id?: number
  site?: QoderSite
}

export interface QoderExchangeCodeRequest {
  session_id: string
  state: string
  code?: string
  callback_url?: string
}

export interface QoderTokenInfo {
  security_oauth_token?: string
  refresh_token?: string
  machine_id?: string
  machine_token?: string
  machine_type?: string
  uid?: string
  aid?: string
  organization_id?: string
  organization_name?: string
  name?: string
  user_type?: string
  site?: QoderSite
  refresh_mode?: 'cosy' | 'qodercn20'
  expires_at?: string
  extra?: Record<string, unknown>
  [key: string]: unknown
}

export interface QoderPollRequest {
  session_id: string
  state: string
}

export interface QoderPollResponse {
  status: 'pending' | 'completed'
  token_info?: QoderTokenInfo
}

export async function generateAuthUrl(
  payload: QoderAuthUrlRequest
): Promise<QoderAuthUrlResponse> {
  const { data } = await apiClient.post<QoderAuthUrlResponse>(
    '/admin/qoder/oauth/auth-url',
    payload
  )
  return data
}

export async function exchangeCode(payload: QoderExchangeCodeRequest): Promise<QoderTokenInfo> {
  const { data } = await apiClient.post<QoderTokenInfo>(
    '/admin/qoder/oauth/exchange-code',
    payload
  )
  return data
}

export async function poll(payload: QoderPollRequest): Promise<QoderPollResponse> {
  const { data } = await apiClient.post<QoderPollResponse>(
    '/admin/qoder/oauth/poll',
    payload
  )
  return data
}

export default { generateAuthUrl, exchangeCode, poll }
