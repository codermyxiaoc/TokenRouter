import type { GroupAvailabilityProbeProtocol, GroupPlatform } from '@/types'

// 与后端的平台校验保持一致，不把网关桥接能力当成上游原生探测能力。
const platformProtocols: Partial<Record<GroupPlatform, GroupAvailabilityProbeProtocol[]>> = {
  openai: ['chat_completions', 'responses'],
  anthropic: ['anthropic'],
  gemini: ['gemini'],
  grok: ['responses'],
  kimi: ['chat_completions', 'responses', 'anthropic'],
  deepseek: ['chat_completions', 'responses', 'anthropic'],
  minimax: ['chat_completions', 'responses', 'anthropic'],
  opencode_go: ['chat_completions', 'responses', 'anthropic'],
  zhipu: ['chat_completions', 'anthropic'],
}

export function getAvailabilityProbeProtocols(platform: GroupPlatform): GroupAvailabilityProbeProtocol[] {
  return ['auto', ...(platformProtocols[platform] ?? [])]
}
