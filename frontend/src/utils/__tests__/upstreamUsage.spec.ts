import { describe, expect, it } from 'vitest'
import type { Account } from '@/types'
import { nativeUpstreamUsageAdapter, effectiveUpstreamUsageAdapter } from '../upstreamUsage'

// 原生探测的官方域名限制不能被相似域名或中继配置绕过。
describe('new platform upstream usage', () => {
  it.each([['cline', 'api.cline.bot'], ['command_code', 'api.commandcode.ai']] as const)('%s 只在官方域名启用原生探测', (platform, host) => {
    const account = { platform, type: 'apikey', credentials: {}, extra: {} } as Account
    expect(nativeUpstreamUsageAdapter(account)).toBe(platform)
    account.credentials = { base_url: `https://${host}/api/v1` }
    expect(nativeUpstreamUsageAdapter(account)).toBe(platform)
    account.credentials = { base_url: `https://${host}.example.test/v1` }
    expect(nativeUpstreamUsageAdapter(account)).toBeNull()
    account.extra = { upstream_usage_query: { enabled: true, adapter: 'new_api' } }
    expect(effectiveUpstreamUsageAdapter(account)).toBe('new_api')
  })
})
