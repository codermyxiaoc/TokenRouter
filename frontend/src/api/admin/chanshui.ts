import { apiClient } from '../client'

// 掺水检测只有管理员入口，账号凭据不会进入浏览器响应或提交体。
export const chanshuiSections = ['fingerprint', 'injection', 'cache', 'tools', 'web', 'knowledge', 'thinking', 'iq'] as const
export type ChanshuiSection = typeof chanshuiSections[number]
export type ChanshuiProtocol = 'auto' | 'chat' | 'anthropic' | 'openai'
export interface ChanshuiTarget {
  id: number
  name: string
  platform: string
  type: string
  supported: boolean
  reason?: string
  warning?: string
  models: string[]
}
export interface ChanshuiReport {
  probes?: { id: string; name: string; status: string; ms?: number; summary?: string }[]
  parts?: { key: string; name?: string; applicable: boolean; known: boolean; points: number; max_points: number; coefficient: number; contribution: number }[]
  fingerprint?: { requested_family: string; family: string; samples: number; low_confidence: boolean; matches: boolean | null }
  skipped?: { id: string; reason: string }[]
}
export interface ChanshuiRun {
  id: string
  config_id: number
  account_id: number
  model: string
  status: 'queued' | 'submitting' | 'running' | 'completed' | 'error' | 'unknown'
  verdict: 'pending' | 'clean' | 'adulterated' | 'unknown' | 'error'
  score?: number | null
  score_complete: boolean
  summary?: string
  report?: ChanshuiReport
  error_message?: string
  action_status: 'none' | 'disabled' | 'recovery_pending' | 'recovered' | 'skipped_changed' | 'skipped_disabled' | 'error'
  action_reason?: string
  created_at: string
  updated_at: string
  finished_at?: string | null
}
export interface ChanshuiConfig {
  id: number
  account_id: number
  account_name: string
  model: string
  protocol: ChanshuiProtocol
  sections: ChanshuiSection[]
  timeout_seconds: number
  enabled: boolean
  schedule_enabled: boolean
  interval_minutes: number
  disable_on_adulteration: boolean
  auto_recover: boolean
  recovery_pending?: boolean
  minimum_score: number | null
  next_run_at?: string | null
  created_at: string
  updated_at: string
  latest_run?: ChanshuiRun | null
  recent_runs?: ChanshuiRun[]
}
export type ChanshuiConfigInput = Pick<ChanshuiConfig, 'account_id' | 'model' | 'protocol' | 'sections' | 'timeout_seconds' | 'enabled' | 'schedule_enabled' | 'interval_minutes' | 'disable_on_adulteration' | 'auto_recover' | 'minimum_score'>
const root = '/admin/chanshui-tests'
export const chanshuiAPI = {
  async configs(signal?: AbortSignal): Promise<{ enabled: boolean; configs: ChanshuiConfig[] }> {
    return (await apiClient.get(root, { signal })).data
  },
  async targets(signal?: AbortSignal): Promise<ChanshuiTarget[]> {
    return (await apiClient.get(`${root}/targets`, { signal })).data
  },
  async save(input: ChanshuiConfigInput, id?: number): Promise<ChanshuiConfig> {
    return id ? (await apiClient.put(`${root}/${id}`, input)).data : (await apiClient.post(root, input)).data
  },
  async remove(id: number): Promise<void> { await apiClient.delete(`${root}/${id}`) },
  async run(id: number): Promise<ChanshuiRun> { return (await apiClient.post(`${root}/${id}/run`)).data },
  async history(id: number, signal?: AbortSignal): Promise<ChanshuiRun[]> { return (await apiClient.get(`${root}/${id}/runs`, { signal })).data },
  async detail(id: string, signal?: AbortSignal): Promise<ChanshuiRun> { return (await apiClient.get(`${root}/runs/${encodeURIComponent(id)}`, { signal })).data },
}
