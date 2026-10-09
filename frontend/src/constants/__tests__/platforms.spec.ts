import { describe, expect, it } from 'vitest'
import { CONCRETE_PLATFORM_OPTIONS, GROUP_PLATFORM_OPTIONS } from '@/constants/platforms'

// 独立 Video 必须出现在账号和分组筛选中，同时保留原有平台的顺序。
const concretePlatforms = [
  'anthropic',
  'openai',
  'gemini',
  'antigravity',
  'qoder',
  'grok',
  'kimi',
  'zhipu',
  'deepseek',
  'minimax',
  'opencode_go',
  'video',
  'typesafe',
  'cline',
  'command_code'
]

describe('platform option catalogs', () => {
  it('exposes every concrete account platform', () => {
    expect(CONCRETE_PLATFORM_OPTIONS.map((option) => option.value)).toEqual(concretePlatforms)
  })

  it('keeps group filters aligned with concrete platforms in this fork', () => {
    expect(GROUP_PLATFORM_OPTIONS.map((option) => option.value)).toEqual(concretePlatforms)
  })
})
