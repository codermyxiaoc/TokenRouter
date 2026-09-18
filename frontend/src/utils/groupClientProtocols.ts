import type { GroupClientProtocol, GroupPlatform } from '@/types'

export const GROUP_CLIENT_PROTOCOL_ORDER: readonly GroupClientProtocol[] = [
  'anthropic_messages',
  'openai_responses',
  'openai_chat_completions',
  'gemini_generate_content'
]

interface GroupClientProtocolPolicy {
  supported: readonly GroupClientProtocol[]
  defaults: readonly GroupClientProtocol[]
}

const GROUP_CLIENT_PROTOCOL_POLICIES: Record<GroupPlatform, GroupClientProtocolPolicy> = {
  anthropic: {
    supported: ['anthropic_messages', 'openai_responses', 'openai_chat_completions'],
    defaults: ['anthropic_messages']
  },
  openai: {
    supported: ['anthropic_messages', 'openai_responses', 'openai_chat_completions'],
    defaults: ['openai_responses', 'openai_chat_completions']
  },
  gemini: {
    supported: GROUP_CLIENT_PROTOCOL_ORDER,
    defaults: ['gemini_generate_content']
  },
  antigravity: {
    supported: GROUP_CLIENT_PROTOCOL_ORDER,
    defaults: ['anthropic_messages', 'gemini_generate_content']
  },
  qoder: {
    supported: ['anthropic_messages', 'openai_responses', 'openai_chat_completions'],
    defaults: []
  },
  grok: {
    supported: ['anthropic_messages', 'openai_responses', 'openai_chat_completions'],
    defaults: ['openai_responses', 'openai_chat_completions']
  },
  kimi: {
    supported: ['anthropic_messages', 'openai_responses', 'openai_chat_completions'],
    defaults: ['anthropic_messages', 'openai_responses', 'openai_chat_completions']
  },
  zhipu: {
    supported: ['anthropic_messages', 'openai_responses', 'openai_chat_completions'],
    defaults: ['anthropic_messages', 'openai_responses', 'openai_chat_completions']
  },
  deepseek: {
    supported: ['anthropic_messages', 'openai_responses', 'openai_chat_completions'],
    defaults: ['anthropic_messages', 'openai_responses', 'openai_chat_completions']
  },
  // OpenCode 可接入三种客户端协议，由账号模型规则选择上游。
  opencode_go: {
    supported: ['anthropic_messages', 'openai_responses', 'openai_chat_completions'],
    defaults: ['anthropic_messages', 'openai_responses', 'openai_chat_completions']
  },
  minimax: {
    supported: ['anthropic_messages', 'openai_responses', 'openai_chat_completions'],
    defaults: ['anthropic_messages', 'openai_responses', 'openai_chat_completions']
  }
}

function orderedProtocols(protocols: Iterable<GroupClientProtocol>): GroupClientProtocol[] {
  const selected = new Set(protocols)
  return GROUP_CLIENT_PROTOCOL_ORDER.filter((protocol) => selected.has(protocol))
}

export function supportedGroupClientProtocols(platform: GroupPlatform): GroupClientProtocol[] {
  return orderedProtocols(GROUP_CLIENT_PROTOCOL_POLICIES[platform].supported)
}

export function defaultGroupClientProtocols(platform: GroupPlatform): GroupClientProtocol[] {
  return orderedProtocols(GROUP_CLIENT_PROTOCOL_POLICIES[platform].defaults)
}

// 过滤不受平台支持的值，并保持公共契约规定的固定顺序。
export function effectiveGroupClientProtocols(
  platform: GroupPlatform,
  protocols: readonly GroupClientProtocol[] | null | undefined
): GroupClientProtocol[] {
  const supported = new Set(GROUP_CLIENT_PROTOCOL_POLICIES[platform].supported)
  return orderedProtocols((protocols ?? []).filter((protocol) => supported.has(protocol)))
}

export function hasGroupClientProtocol(
  protocols: readonly GroupClientProtocol[],
  protocol: GroupClientProtocol
): boolean {
  return protocols.includes(protocol)
}

export function setGroupClientProtocol(
  platform: GroupPlatform,
  protocols: readonly GroupClientProtocol[],
  protocol: GroupClientProtocol,
  enabled: boolean
): GroupClientProtocol[] {
  const policy = GROUP_CLIENT_PROTOCOL_POLICIES[platform]
  if (!policy.supported.includes(protocol)) {
    return effectiveGroupClientProtocols(platform, [...protocols])
  }

  const next = new Set(protocols)
  if (enabled) {
    next.add(protocol)
  } else {
    next.delete(protocol)
  }
  return effectiveGroupClientProtocols(platform, [...next])
}
