import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import OpenAIQuotaResetCell from '../OpenAIQuotaResetCell.vue'
import type { Account } from '@/types'
import { refreshOpenAIQuota } from '@/api/admin/accounts'

vi.mock('@/api/admin/accounts', () => ({
  refreshOpenAIQuota: vi.fn(),
  resetOpenAIQuota: vi.fn(),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

function makeAccount(overrides: Partial<Account>): Account {
  return {
    id: 1,
    name: 'acc',
    platform: 'openai',
    type: 'oauth',
    proxy_id: null,
    concurrency: 3,
    priority: 50,
    status: 'active',
    error_message: null,
    last_used_at: null,
    expires_at: null,
    auto_pause_on_expired: false,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    schedulable: true,
    rate_limited_at: null,
    rate_limit_reset_at: null,
    overload_until: null,
    temp_unschedulable_until: null,
    temp_unschedulable_reason: null,
    session_window_start: null,
    session_window_end: null,
    session_window_status: null,
    ...overrides,
  }
}

beforeEach(() => {
  vi.mocked(refreshOpenAIQuota).mockReset()
})

describe('OpenAIQuotaResetCell — 影子账号和缓存展示', () => {
  it('影子账号(parent_account_id 非空)可以查询，但重置入口禁用', () => {
    const account = makeAccount({ parent_account_id: 100 })
    const wrapper = mount(OpenAIQuotaResetCell, { props: { account } })

    const buttons = wrapper.findAll('button').filter(button => !button.attributes('data-testid')?.startsWith('referral-') && button.attributes('data-testid') !== 'codex-credits')
    expect(buttons).toHaveLength(2)
    expect(buttons[0].attributes('title')).toBe('admin.accounts.openaiQuotaReset.countTooltipLoad')
    expect(wrapper.get('[data-testid="reset-quota"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('普通账号(无 parent_account_id)实时查询之前禁止重置', () => {
    const account = makeAccount({ parent_account_id: null })
    const wrapper = mount(OpenAIQuotaResetCell, { props: { account } })

    const buttons = wrapper.findAll('button').filter(button => !button.attributes('data-testid')?.startsWith('referral-') && button.attributes('data-testid') !== 'codex-credits')
    expect(buttons).toHaveLength(2)
    expect(buttons[0].attributes('title')).toBe('admin.accounts.openaiQuotaReset.countTooltipLoad')
    expect(wrapper.get('[data-testid="reset-quota"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('从账号 extra 恢复未过期的次数和到期明细', () => {
    const account = makeAccount({
      extra: {
        codex_reset_credit_snapshot: {
          available_count: 2,
          credits: [
            { expires_at: '2099-07-05T04:05:06Z' },
            { expires_at: '2099-07-03T04:05:06Z' },
          ],
        },
      },
    })

    const wrapper = mount(OpenAIQuotaResetCell, { props: { account } })

    expect(refreshOpenAIQuota).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('admin.accounts.openaiQuotaReset.count2')
    expect(wrapper.text()).toContain('admin.accounts.openaiQuotaReset.expiresAt')
    expect(wrapper.text()).toContain('+1')
    // 缓存能够展示到期明细，但不能代替实时查询来获得重置资格。
    expect(wrapper.get('[data-testid="reset-quota"]').attributes('disabled')).toBeDefined()
    expect(wrapper.findAll('button').filter(button => !button.attributes('data-testid')?.startsWith('referral-') && button.attributes('data-testid') !== 'codex-credits')).toHaveLength(3)
    wrapper.unmount()
  })

  it('缓存次数全部过期时视为未知', () => {
    const account = makeAccount({
      extra: {
        codex_reset_credit_snapshot: {
          available_count: 1,
          credits: [{ expires_at: '2020-07-03T04:05:06Z' }],
        },
      },
    })

    const wrapper = mount(OpenAIQuotaResetCell, { props: { account } })

    expect(wrapper.text()).not.toContain('admin.accounts.openaiQuotaReset.count1')
    expect(wrapper.text()).not.toContain('admin.accounts.openaiQuotaReset.expiresAt')
    expect(wrapper.findAll('button').filter(button => !button.attributes('data-testid')?.startsWith('referral-') && button.attributes('data-testid') !== 'codex-credits')).toHaveLength(2)
    wrapper.unmount()
  })

  it('快照持久化失败时仍显示实时次数并告警', async () => {
    vi.mocked(refreshOpenAIQuota).mockResolvedValue({
      rate_limit_reset_credits: { available_count: 2 },
      fetched_at: 1770000000,
      cache_persisted: false,
    })
    const wrapper = mount(OpenAIQuotaResetCell, {
      props: { account: makeAccount({ parent_account_id: null }) },
    })

    await wrapper.get('button').trigger('click')
    await flushPromises()

    expect(refreshOpenAIQuota).toHaveBeenCalledWith(1)
    expect(wrapper.text()).toContain('admin.accounts.openaiQuotaReset.count2')
    expect(wrapper.text()).toContain('admin.accounts.openaiQuotaReset.refreshCachePersistFailed')
    expect(wrapper.findAll('button').filter(button => !button.attributes('data-testid')?.startsWith('referral-') && button.attributes('data-testid') !== 'codex-credits')).toHaveLength(2)
    wrapper.unmount()
  })
})

// 积分独立于重置次数，已有余额不能直接解锁真实重置入口。
describe('Codex积分展示', () => {
 it('保留小数精度且不触发自动网络请求', () => {
  const account=makeAccount({extra:{codex_credits_snapshot:{credits:{has_credits:true,unlimited:false,balance:'12345678901234567890.0123'},fetched_at:123}}})
  const wrapper=mount(OpenAIQuotaResetCell,{props:{account}})
  expect(wrapper.get('[data-testid="codex-credits"]').text()).toContain('12345678901234567890.0123')
  expect(refreshOpenAIQuota).not.toHaveBeenCalled()
  expect(wrapper.get('[data-testid="reset-quota"]').attributes('disabled')).toBeDefined()
  wrapper.unmount()
 })
 it('成功查询缺失积分时清除旧余额', async()=>{
  vi.mocked(refreshOpenAIQuota).mockResolvedValue({fetched_at:456,cache_persisted:true})
  const account=makeAccount({extra:{codex_credits_snapshot:{credits:{has_credits:true,unlimited:false,balance:'20.1'},fetched_at:123}}})
  const wrapper=mount(OpenAIQuotaResetCell,{props:{account}})
  await wrapper.get('[data-testid="codex-credits"]').trigger('click');await flushPromises()
  expect(wrapper.get('[data-testid="codex-credits"]').text()).toContain('—')
  expect(wrapper.get('[data-testid="codex-credits"]').text()).not.toContain('20.1')
  wrapper.unmount()
 })
})
