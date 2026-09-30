import type { Group, GroupClientProtocol, GroupPlatform } from '@/types'

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

// 仅展示已验证整条降级链的非 Claude Code 入口；availableGroups 必须先按用户和套餐权限过滤。
export function claudeCodeFallbackProtocols(
  source: Pick<Group, 'id' | 'platform' | 'allowed_client_protocols' | 'fallback_group_id'>,
  availableGroups: readonly Group[]
): GroupClientProtocol[] {
  if (source.platform !== 'anthropic' && source.platform !== 'antigravity') return []
  const groups = new Map(availableGroups.map(group => [group.id, group]))
  return effectiveGroupClientProtocols(source.platform, source.allowed_client_protocols).filter(protocol => {
    // Gemini 原生入口没有对应的 Claude-only 兼容降级处理器。
    if (protocol === 'gemini_generate_content') return false
    const visited = new Set([source.id])
    let nextID = source.fallback_group_id
    while (nextID && nextID > 0 && !visited.has(nextID)) {
      visited.add(nextID)
      const group = groups.get(nextID)
      if (!group || group.status !== 'active' ||
        (group.platform !== 'anthropic' && group.platform !== 'antigravity') ||
        !effectiveGroupClientProtocols(group.platform, group.allowed_client_protocols).includes(protocol)) return false
      if (!group.claude_code_only) return true
      nextID = group.fallback_group_id
    }
    return false
  })
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
