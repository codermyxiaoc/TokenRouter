import { afterEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, mount } from '@vue/test-utils'
import AccountUpstreamUsageCell from '../AccountUpstreamUsageCell.vue'
import { nativeUpstreamUsageAdapter } from '@/utils/upstreamUsage'

vi.mock('vue-i18n', async () => ({ ...await vi.importActual('vue-i18n'), useI18n: () => ({ t: (key: string, params?: Record<string, unknown> | string) =>
  typeof params === 'string' ? params : `${key}:${Object.values(params ?? {}).join('|')}` }) }))
enableAutoUnmount(afterEach)
const account = { id: 17, name: 'test', type: 'apikey', platform: 'cline', credentials: {}, extra: {} } as any

describe('供应商响应及链接攻击输入', () => {
  it.each(['<img src=x onerror=alert(1)>', '<script>alert(1)</script>', 'javascript:alert(1)',
    'https://attacker.invalid/?token=not-a-token', '"><svg/onload=alert(1)>'])('恶意供应商文本 %s 只作为文本渲染', (payload) => {
    const wrapper = mount(AccountUpstreamUsageCell, { props: { account, result: {
      account_id: 17, adapter: 'cline', observed_at: '2026-10-09T00:00:00Z', provider: 'cline', mode: 'balance',
      balances: [{ currency: payload, remaining: 12 }],
      subscription: { plan_name: payload, unlimited: false, expires_at: payload },
      limits: [{ name: payload, used: 1, limit: 10, remaining: 9 }],
    } }, global: { stubs: { Icon: true, UsageProgressBar: { props: ['label'], template: '<div>{{ label }}</div>' } } } })
    expect(wrapper.text()).toContain(payload)
    expect(wrapper.find('a,img,script,svg,iframe').exists()).toBe(false)
  })

  it.each(['<img src=x onerror=alert(1)>', 'javascript:alert(1)'])('错误消息 %s 不转换成 HTML 或链接', (payload) => {
    const wrapper = mount(AccountUpstreamUsageCell, { props: { account, error: { message: payload } }, global: { stubs: { Icon: true } } })
    expect(wrapper.text()).toContain(payload)
    expect(wrapper.find('a,img,script,iframe').exists()).toBe(false)
  })

  it.each(['https://api.cline.bot.attacker.invalid', 'https://api.cline.bot@attacker.invalid',
    'https://attacker.invalid/api.cline.bot', 'https://attacker.invalid/?url=https://api.cline.bot',
    'javascript:api.cline.bot', 'not-a-url'])('相似官方地址 %s 不启用原生钱包', (base_url) => {
    expect(nativeUpstreamUsageAdapter({ ...account, credentials: { base_url } })).toBeNull()
  })
})
