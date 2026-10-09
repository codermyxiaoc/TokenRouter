import { describe, expect, it } from 'vitest'
import type { Group, GroupPlatform } from '@/types'
import {
  defaultGroupClientProtocols,
  claudeCodeFallbackProtocols,
  effectiveGroupClientProtocols,
  setGroupClientProtocol,
  supportedGroupClientProtocols
} from '../groupClientProtocols'

describe('groupClientProtocols', () => {
  it('Claude-only 降级需要整条可访问链、协议交集和最终非限制分组', () => {
    const source = { id: 1, platform: 'anthropic', fallback_group_id: 2,
      allowed_client_protocols: ['anthropic_messages', 'openai_responses'] } as Group
    const middle = { ...source, id: 2, status: 'active', claude_code_only: true, fallback_group_id: 3 } as Group
    const target = { ...source, id: 3, status: 'active', claude_code_only: false,
      allowed_client_protocols: ['openai_responses'] } as Group
    expect(claudeCodeFallbackProtocols(source, [middle, target])).toEqual(['openai_responses'])
    expect(claudeCodeFallbackProtocols(source, [middle])).toEqual([])
    expect(claudeCodeFallbackProtocols(source, [middle, { ...target, status: 'inactive' }])).toEqual([])
    expect(claudeCodeFallbackProtocols(source, [{ ...middle, fallback_group_id: 1 }, target])).toEqual([])
    expect(claudeCodeFallbackProtocols(source, [middle, { ...target, platform: 'openai' }])).toEqual([])
    expect(claudeCodeFallbackProtocols({ ...source, allowed_client_protocols: [] }, [middle, target])).toEqual([])
  })

  it.each<[GroupPlatform, string[], string[]]>([
    ['anthropic', ['anthropic_messages', 'openai_responses', 'openai_chat_completions'], ['anthropic_messages']],
    ['openai', ['anthropic_messages', 'openai_responses', 'openai_chat_completions'], ['openai_responses', 'openai_chat_completions']],
    ['gemini', ['anthropic_messages', 'openai_responses', 'openai_chat_completions', 'gemini_generate_content'], ['gemini_generate_content']],
    ['antigravity', ['anthropic_messages', 'openai_responses', 'openai_chat_completions', 'gemini_generate_content'], ['anthropic_messages', 'gemini_generate_content']],
    ['typesafe', [], []],
    ['cline', ['anthropic_messages', 'openai_responses', 'openai_chat_completions'], ['anthropic_messages', 'openai_responses', 'openai_chat_completions']],
    ['command_code', ['anthropic_messages', 'openai_responses', 'openai_chat_completions'], ['anthropic_messages', 'openai_responses', 'openai_chat_completions']],
    ['qoder', ['anthropic_messages', 'openai_responses', 'openai_chat_completions'], []],
    ['grok', ['anthropic_messages', 'openai_responses', 'openai_chat_completions'], ['openai_responses', 'openai_chat_completions']],
    ['kimi', ['anthropic_messages', 'openai_responses', 'openai_chat_completions'], ['anthropic_messages', 'openai_responses', 'openai_chat_completions']],
    ['zhipu', ['anthropic_messages', 'openai_responses', 'openai_chat_completions'], ['anthropic_messages', 'openai_responses', 'openai_chat_completions']],
    ['deepseek', ['anthropic_messages', 'openai_responses', 'openai_chat_completions'], ['anthropic_messages', 'openai_responses', 'openai_chat_completions']],
    ['minimax', ['anthropic_messages', 'openai_responses', 'openai_chat_completions'], ['anthropic_messages', 'openai_responses', 'openai_chat_completions']]
  ])('returns the %s protocol policy', (platform, supported, defaults) => {
    expect(supportedGroupClientProtocols(platform)).toEqual(supported)
    expect(defaultGroupClientProtocols(platform)).toEqual(defaults)
  })

  it('OpenCode 默认允许三种文本协议，并过滤 Gemini 协议', () => {
    expect(defaultGroupClientProtocols('opencode_go')).toEqual(['anthropic_messages', 'openai_responses', 'openai_chat_completions'])
    expect(effectiveGroupClientProtocols('opencode_go', ['gemini_generate_content', 'openai_responses'])).toEqual(['openai_responses'])
  })

  it('treats missing and explicit empty collections as no enabled protocols', () => {
    expect(effectiveGroupClientProtocols('openai', undefined)).toEqual([])
    expect(effectiveGroupClientProtocols('qoder', [])).toEqual([])
  })

  it('allows every supported protocol to be disabled', () => {
    expect(setGroupClientProtocol('openai', ['openai_responses', 'openai_chat_completions'], 'openai_responses', false)).toEqual([
      'openai_chat_completions'
    ])
    expect(setGroupClientProtocol('openai', ['openai_chat_completions'], 'openai_chat_completions', false)).toEqual([])
  })
})
