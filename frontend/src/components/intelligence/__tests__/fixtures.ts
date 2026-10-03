import type { IntelligenceConfig, IntelligenceRun, IntelligenceTest } from '@/api/intelligence'

// 夹具只模拟检测协议，不包含可调用密钥或真实上游地址。
export const runFixture = (overrides: Partial<IntelligenceRun> = {}): IntelligenceRun => ({
  id: 'run-1', config_id: 1, group_id: 1, model: 'model-a', benchmark: 'candy',
  status: 'completed', verdict: 'passed', has_detail: true, has_artifact: false,
  created_at: '2026-10-03T01:00:00Z', updated_at: '2026-10-03T01:01:00Z', ...overrides,
})
export const testFixture = (overrides: Partial<IntelligenceTest> = {}): IntelligenceTest => ({
  id: 1, group_id: 1, group_name: 'Group A', model: 'model-a', benchmark: 'candy', runs: [], artifacts: [], ...overrides,
})
export const configFixture = (overrides: Partial<IntelligenceConfig> = {}): IntelligenceConfig => ({
  id: 1, group_id: 1, group_name: 'Group A', model: 'model-a', benchmark: 'candy',
  base_url: 'https://gateway.example/v1', api_key_configured: true, protocol: 'responses',
  reasoning_effort: '', service_tier: '', enabled: true, schedule_enabled: false, interval_minutes: 60,
  created_at: '2026-10-03T01:00:00Z', updated_at: '2026-10-03T01:00:00Z', ...overrides,
})
