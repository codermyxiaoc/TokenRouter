import type { AccountPlatform, AccountType } from '@/types'

// 只对具有 API Key 转发链路的平台展示声明探测，不能用中继地址推断平台。
export function isUpstreamBillingEligible(account: { platform: AccountPlatform; type: AccountType }): boolean {
  return account.type === 'apikey' && !['antigravity', 'qoder'].includes(account.platform)
}

export interface UpstreamBillingFlags {
  probe: boolean
  sync: boolean
}

// 同步依赖自动探测；关掉自动探测时也必须关掉同步，反向关闭同步不影响探测。
export function updateUpstreamBillingFlags(flags: UpstreamBillingFlags, field: 'probe' | 'sync', enabled: boolean): UpstreamBillingFlags {
  if (field === 'sync') return { probe: enabled || flags.probe, sync: enabled }
  return { probe: enabled, sync: enabled && flags.sync }
}
