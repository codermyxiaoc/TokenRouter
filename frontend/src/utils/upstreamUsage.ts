import type { Account } from '@/types'

type UpstreamUsageAccount = Pick<Account, 'type' | 'platform' | 'credentials' | 'extra'>

/** 官方余额与套餐窗口固定使用原生协议；按量中继账号可单独选择通用查询协议。 */
export function nativeUpstreamUsageAdapter(account: UpstreamUsageAccount): string | null {
  if (account.type !== 'apikey') return null
  const rawMode = account.credentials?.account_mode
  const mode = typeof rawMode === 'string' ? rawMode.trim() : ''
  switch (account.platform) {
    case 'kimi': return mode === 'coding' ? 'kimi_coding' : 'kimi_balance'
    case 'zhipu': return mode === 'coding' ? 'zhipu_coding' : null
    case 'minimax': return mode === 'coding' ? 'minimax_coding' : null
    case 'deepseek': return mode === 'coding' ? null : 'deepseek_balance'
    case 'opencode_go': return mode === 'zen' ? null : 'opencode_go'
    default: return null
  }
}

/** 列表缓存与表单使用同一适配器规则；历史 Zen 的 GO 配置回退为 Sub2API。 */
export function effectiveUpstreamUsageAdapter(account: UpstreamUsageAccount): string {
  const nativeAdapter = nativeUpstreamUsageAdapter(account)
  if (nativeAdapter) return nativeAdapter
  const config = account.extra?.upstream_usage_query as Record<string, unknown> | undefined
  const adapter = typeof config?.adapter === 'string' ? config.adapter.trim() : ''
  return adapter === 'new_api' || adapter === 'zivv' ? adapter : 'sub2api'
}

/** 查询限于 API Key；DeepSeek Coding 不是合法的账号组合。 */
export function supportsUpstreamUsageQuery(account: UpstreamUsageAccount): boolean {
  return account.type === 'apikey' &&
    !(account.platform === 'deepseek' && typeof account.credentials?.account_mode === 'string' &&
      account.credentials.account_mode.trim() === 'coding')
}

/** 展示、按钮和单次/批量查询共用开关，避免关闭或不支持的账号仍能发起请求。 */
export function isUpstreamUsageQueryEnabled(account: UpstreamUsageAccount): boolean {
  const config = account.extra?.upstream_usage_query as Record<string, unknown> | undefined
  return supportsUpstreamUsageQuery(account) && config?.enabled !== false
}
