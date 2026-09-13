import { describe, expect, it } from 'vitest'
import type { GroupPlatform } from '@/types'
import {
  defaultGroupClientProtocols,
  effectiveGroupClientProtocols,
  setGroupClientProtocol,
  supportedGroupClientProtocols
} from '../groupClientProtocols'

describe('groupClientProtocols', () => {
  it.each<[GroupPlatform, string[], string[]]>([
    ['anthropic', ['anthropic_messages', 'openai_responses', 'openai_chat_completions'], ['anthropic_messages']],
    ['openai', ['anthropic_messages', 'openai_responses', 'openai_chat_completions'], ['openai_responses', 'openai_chat_completions']],
    ['gemini', ['anthropic_messages', 'openai_responses', 'openai_chat_completions', 'gemini_generate_content'], ['gemini_generate_content']],
    ['antigravity', ['anthropic_messages', 'openai_responses', 'openai_chat_completions', 'gemini_generate_content'], ['anthropic_messages', 'gemini_generate_content']],
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
