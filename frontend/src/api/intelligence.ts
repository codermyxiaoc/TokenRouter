import { apiClient } from './client'

// 检测结果与配置使用独立接口；浏览器从不直接携带站内测试密钥访问检测供应商。
export type IntelligenceBenchmark = 'candy' | 'drawing'
export type IntelligenceVerdict = 'pending' | 'passed' | 'failed' | 'error' | 'unknown'
export interface IntelligenceRun {
  id: string
  config_id: number
  group_id: number
  model: string
  benchmark: IntelligenceBenchmark
  status: 'queued' | 'submitting' | 'running' | 'completed' | 'error' | 'unknown'
  verdict: IntelligenceVerdict
  phase?: string
  question?: string
  answer?: string
  html?: string
  input_tokens?: number | null
  output_tokens?: number | null
  reasoning_tokens?: number | null
  duration_ms?: number | null
  assessment_reason?: string
  error_message?: string
  has_detail?: boolean
  has_artifact?: boolean
  created_at: string
  updated_at: string
  finished_at?: string | null
}

export interface IntelligenceConfig {
  id: number
  group_id: number
  group_name: string
  model: string
  benchmark: IntelligenceBenchmark
  base_url: string
  api_key_configured: boolean
  protocol: 'responses' | 'chat_completions'
  reasoning_effort: string
  service_tier: string
  enabled: boolean
  schedule_enabled: boolean
  interval_minutes: number
  next_run_at?: string | null
  created_at: string
  updated_at: string
  latest_run?: IntelligenceRun | null
}

export type IntelligenceConfigInput = Pick<IntelligenceConfig,
  'group_id' | 'model' | 'benchmark' | 'base_url' | 'protocol' | 'reasoning_effort' |
  'service_tier' | 'enabled' | 'schedule_enabled' | 'interval_minutes'> & { api_key: string }

export interface IntelligenceTest {
  id: number
  group_id: number
  group_name: string
  model: string
  benchmark: IntelligenceBenchmark
  runs: IntelligenceRun[]
  artifacts: IntelligenceRun[]
}

export interface IntelligencePreview {
  url: string
  expires_at: string
}

export const intelligenceAPI = {
  async list(signal?: AbortSignal): Promise<IntelligenceTest[]> {
    return (await apiClient.get<IntelligenceTest[]>('/intelligence-tests', { signal })).data
  },
  async detail(id: string, admin = false, signal?: AbortSignal): Promise<IntelligenceRun> {
    return (await apiClient.get<IntelligenceRun>(`${admin ? '/admin' : ''}/intelligence-tests/runs/${encodeURIComponent(id)}`, { signal })).data
  },
  async preview(id: string, admin = false, signal?: AbortSignal): Promise<IntelligencePreview> {
    return (await apiClient.get<IntelligencePreview>(`${admin ? '/admin' : ''}/intelligence-tests/runs/${encodeURIComponent(id)}/preview`, { signal })).data
  },
  async configs(signal?: AbortSignal): Promise<IntelligenceConfig[]> {
    return (await apiClient.get<IntelligenceConfig[]>('/admin/intelligence-tests', { signal })).data
  },
  async save(input: IntelligenceConfigInput, id?: number): Promise<IntelligenceConfig> {
    return id
      ? (await apiClient.put<IntelligenceConfig>(`/admin/intelligence-tests/${id}`, input)).data
      : (await apiClient.post<IntelligenceConfig>('/admin/intelligence-tests', input)).data
  },
  async remove(id: number): Promise<void> {
    await apiClient.delete(`/admin/intelligence-tests/${id}`)
  },
  async run(id: number): Promise<IntelligenceRun> {
    return (await apiClient.post<IntelligenceRun>(`/admin/intelligence-tests/${id}/run`)).data
  },
  async history(id: number, signal?: AbortSignal): Promise<IntelligenceRun[]> {
    return (await apiClient.get<IntelligenceRun[]>(`/admin/intelligence-tests/${id}/runs`, { signal })).data
  },
}
