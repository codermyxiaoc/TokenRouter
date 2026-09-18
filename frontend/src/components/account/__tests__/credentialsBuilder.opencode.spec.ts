import { describe, expect, it } from 'vitest'
import {
  applyOpenCodeGoProtocolRules, cloneOpenCodeGoProtocolRules, defaultOpenCodeProtocolRules,
  defaultCNAdaptiveBaseUrls, isHeaderOverrideCapable, parseOpenCodeGoProtocolRules,
  resolveOpenCodeAccountMode
} from '../credentialsBuilder'

// 覆盖凭据边界：缺省、显式清空、模式切换与共享数组不能互相污染。
describe('OpenCode credentials', () => {
  it.each(['zen', 'go'] as const)('resolves native protocol endpoints for %s', mode => {
    const base = mode === 'zen' ? 'https://opencode.ai/zen' : 'https://opencode.ai/zen/go'
    expect(defaultCNAdaptiveBaseUrls('opencode_go', mode)).toEqual({ chat_completions: `${base}/v1`, responses: `${base}/v1`, anthropic: base })
  })

  it('distinguishes absent rules from an explicitly empty list', () => {
    expect(parseOpenCodeGoProtocolRules(undefined)).toBeNull()
    expect(parseOpenCodeGoProtocolRules(null)).toBeNull()
    expect(parseOpenCodeGoProtocolRules({})).toBeNull()
    expect(parseOpenCodeGoProtocolRules([])).toEqual([])
    for (const mode of ['create', 'edit'] as const) {
      const credentials = {}
      applyOpenCodeGoProtocolRules(credentials, [], mode)
      expect(credentials).toEqual({ protocol_rules: [] })
    }
  })

  it('normalizes patterns, retains order and filters malformed stored rows', () => {
    const rules = parseOpenCodeGoProtocolRules([{ pattern: ' GPT-* ', protocol: 'responses' }, { pattern: '*', protocol: 'adaptive' }, { pattern: '', protocol: 'anthropic' }, { pattern: 'qwen*', protocol: 'anthropic' }])!
    const credentials = {}
    applyOpenCodeGoProtocolRules(credentials, rules, 'edit')
    expect(credentials).toEqual({ protocol_rules: [{ pattern: 'gpt-*', protocol: 'responses' }, { pattern: 'qwen*', protocol: 'anthropic' }] })
  })

  it('keeps mode defaults isolated and supports API key header overrides', () => {
    const rules = cloneOpenCodeGoProtocolRules(defaultOpenCodeProtocolRules('zen'))
    rules[0].pattern = 'private-*'
    expect(defaultOpenCodeProtocolRules('zen')[0].pattern).toBe('grok-*')
    expect(defaultOpenCodeProtocolRules('go')).toContainEqual({ pattern: 'minimax-*', protocol: 'anthropic' })
    expect(resolveOpenCodeAccountMode(undefined)).toBe('go')
    expect(resolveOpenCodeAccountMode('zen')).toBe('zen')
    expect(isHeaderOverrideCapable('opencode_go', 'apikey')).toBe(true)
    expect(isHeaderOverrideCapable('opencode_go', 'oauth')).toBe(false)
  })
})
