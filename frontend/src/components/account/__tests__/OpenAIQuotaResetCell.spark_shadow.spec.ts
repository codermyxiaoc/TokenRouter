import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import OpenAIQuotaResetCell from '../OpenAIQuotaResetCell.vue'
import type { Account } from '@/types'
import { refreshOpenAIQuota } from '@/api/admin/accounts'

vi.mock('@/api/admin/accounts', () => ({
  refreshOpenAIQuota: vi.fn(),
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

describe('OpenAIQuotaResetCell — fork 查询-only 语义', () => {
  it('影子账号(parent_account_id 非空)只展示查询按钮，不展示重置入口', () => {
    const account = makeAccount({ parent_account_id: 100 })
    const wrapper = mount(OpenAIQuotaResetCell, { props: { account } })

    const buttons = wrapper.findAll('button')
    expect(buttons).toHaveLength(1)
    expect(buttons[0].attributes('title')).toBe('admin.accounts.openaiQuotaReset.countTooltipLoad')
    wrapper.unmount()
  })

  it('普通账号(无 parent_account_id)同样只展示查询按钮', () => {
    const account = makeAccount({ parent_account_id: null })
    const wrapper = mount(OpenAIQuotaResetCell, { props: { account } })

    const buttons = wrapper.findAll('button')
    expect(buttons).toHaveLength(1)
    expect(buttons[0].attributes('title')).toBe('admin.accounts.openaiQuotaReset.countTooltipLoad')
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
    // 第二个按钮仅用于展开到期明细，不是上游重置入口。
    expect(wrapper.findAll('button')).toHaveLength(2)
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
    expect(wrapper.findAll('button')).toHaveLength(1)
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
    expect(wrapper.findAll('button')).toHaveLength(1)
    wrapper.unmount()
  })
})
