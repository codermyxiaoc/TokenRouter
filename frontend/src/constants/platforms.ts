import type { AccountPlatform, GroupPlatform } from '@/types'

export interface PlatformOption<T extends string = string> {
  value: T
  label: string
  [key: string]: unknown
}

// 账号与请求路由支持的具体平台目录；各管理筛选器统一从此处派生。
export const CONCRETE_PLATFORM_OPTIONS = [
  { value: 'anthropic', label: 'Anthropic' },
  { value: 'openai', label: 'OpenAI' },
  { value: 'gemini', label: 'Gemini' },
  { value: 'antigravity', label: 'Antigravity' },
  { value: 'qoder', label: 'Qoder' },
  { value: 'grok', label: 'Grok' },
  { value: 'kimi', label: 'Kimi' },
  { value: 'zhipu', label: 'Zhipu' },
  { value: 'deepseek', label: 'DeepSeek' },
  { value: 'minimax', label: 'MiniMax' },
  { value: 'opencode_go', label: 'OpenCode' },
  { value: 'video', label: 'Video' },
  { value: 'typesafe', label: 'TypeSafe' },
  { value: 'cline', label: 'Cline' },
  { value: 'command_code', label: 'Command Code' }
] as const satisfies readonly PlatformOption<AccountPlatform>[]

// fork 尚未引入 Composite 分组，分组选项与具体平台目录保持一致。
export const GROUP_PLATFORM_OPTIONS = CONCRETE_PLATFORM_OPTIONS as readonly PlatformOption<GroupPlatform>[]

// 账号同步需要可发现的模型端点；渠道同步还允许读取本站 Qoder 固定目录。
export function supportsAccountModelSync(platform: string): boolean {
  return CONCRETE_PLATFORM_OPTIONS.some(option => option.value === platform) && !['qoder', 'video', 'typesafe'].includes(platform)
}

export function supportsChannelModelSync(platform: string): boolean {
  return platform === 'qoder' || supportsAccountModelSync(platform)
}
