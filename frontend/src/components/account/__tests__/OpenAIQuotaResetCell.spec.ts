import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import OpenAIQuotaResetCell from '../OpenAIQuotaResetCell.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import { refreshOpenAIQuota, resetOpenAIQuota, type OpenAIQuotaRefreshResult, type OpenAIQuotaResetResult } from '@/api/admin/accounts'
import type { Account } from '@/types'

vi.mock('@/api/admin/accounts', () => ({ refreshOpenAIQuota: vi.fn(), resetOpenAIQuota: vi.fn() }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

enableAutoUnmount(afterEach)

const makeAccount = (overrides: Partial<Account> = {}) => ({
  id: 1, name: 'OpenAI account', platform: 'openai', type: 'oauth', ...overrides,
}) as Account

const liveQuota: OpenAIQuotaRefreshResult = {
  fetched_at: 1770000000,
  rate_limit_reset_credits: { available_count: 2, credits: [{ expires_at: '2099-07-01T00:00:00Z' }] },
  credits: { has_credits: true, unlimited: false, balance: '12.34' },
  cache_persisted: true,
}

const resetResult = (overrides: Partial<OpenAIQuotaResetResult> = {}): OpenAIQuotaResetResult => ({
  code: 'reset', windows_reset: 2, cache_refreshed: true, account_state_recovered: true,
  quota: { ...liveQuota, rate_limit_reset_credits: { available_count: 1 } },
  account: makeAccount({ status: 'active', rate_limit_reset_at: null }),
  ...overrides,
})

const mountCell = (account = makeAccount()) => mount(OpenAIQuotaResetCell, {
  props: { account }, global: { stubs: { teleport: true, OpenAIReferralCell: true } },
})

// 用可控 Promise 覆盖切账号及重复提交时序，不发出真实的上游请求。
const deferred = <T,>() => {
  let resolve!: (value: T) => void
  let reject!: (reason: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

const query = async (wrapper: ReturnType<typeof mountCell>) => {
  await wrapper.get('[data-testid="reset-credit-query"]').trigger('click')
  await flushPromises()
}

const confirm = async (wrapper: ReturnType<typeof mountCell>) => {
  await wrapper.get('[data-testid="reset-quota"]').trigger('click')
  wrapper.getComponent(ConfirmDialog).vm.$emit('confirm')
  await flushPromises()
}

beforeEach(() => {
  vi.mocked(refreshOpenAIQuota).mockReset().mockResolvedValue(liveQuota)
  vi.mocked(resetOpenAIQuota).mockReset().mockResolvedValue(resetResult())
})

describe('OpenAIQuotaResetCell 真实额度重置', () => {
  it('API Key 账号没有查询和重置入口', () => {
    const wrapper = mountCell(makeAccount({ type: 'apikey' }))
    expect(wrapper.find('button').exists()).toBe(false)
    expect(refreshOpenAIQuota).not.toHaveBeenCalled()
    expect(resetOpenAIQuota).not.toHaveBeenCalled()
  })

  it('实时查询后仍须确认，取消不会消费次数', async () => {
    const wrapper = mountCell()
    expect(wrapper.get('[data-testid="reset-quota"]').attributes('disabled')).toBeDefined()
    await query(wrapper)
    expect(wrapper.get('[data-testid="reset-quota"]').attributes('disabled')).toBeUndefined()
    await wrapper.get('[data-testid="reset-quota"]').trigger('click')
    expect(wrapper.getComponent(ConfirmDialog).props('show')).toBe(true)
    expect(resetOpenAIQuota).not.toHaveBeenCalled()
    wrapper.getComponent(ConfirmDialog).vm.$emit('cancel')
    wrapper.getComponent(ConfirmDialog).vm.$emit('confirm')
    await flushPromises()
    expect(resetOpenAIQuota).not.toHaveBeenCalled()
  })

  it('影子账号即使实时查询到次数也无法重置', async () => {
    const wrapper = mountCell(makeAccount({ parent_account_id: 100 }))
    await query(wrapper)
    expect(wrapper.get('[data-testid="reset-quota"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="reset-quota"]').attributes('title')).toContain('resetTooltipShadow')
    wrapper.getComponent(ConfirmDialog).vm.$emit('confirm')
    await flushPromises()
    expect(resetOpenAIQuota).not.toHaveBeenCalled()
  })

  it('没有剩余次数时不能确认重置', async () => {
    vi.mocked(refreshOpenAIQuota).mockResolvedValue({ ...liveQuota, rate_limit_reset_credits: { available_count: 0 } })
    const wrapper = mountCell()
    await query(wrapper)
    expect(wrapper.get('[data-testid="reset-quota"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="reset-quota"]').attributes('title')).toContain('resetTooltipNoCredits')
  })

  it('重置期间锁定查询和重复确认，成功后刷新次数、积分并通知父组件', async () => {
    const pending = deferred<OpenAIQuotaResetResult>()
    vi.mocked(resetOpenAIQuota).mockReturnValue(pending.promise)
    const wrapper = mountCell()
    await query(wrapper)
    await confirm(wrapper)
    wrapper.getComponent(ConfirmDialog).vm.$emit('confirm')
    expect(wrapper.get('[data-testid="reset-credit-query"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="codex-credits"]').attributes('disabled')).toBeDefined()
    expect(resetOpenAIQuota).toHaveBeenCalledTimes(1)
    expect(resetOpenAIQuota).toHaveBeenCalledWith(1)
    const result = resetResult()
    pending.resolve(result)
    await flushPromises()
    expect(wrapper.get('[data-testid="reset-credit-query"]').text()).toContain('1')
    expect(wrapper.get('[data-testid="codex-credits"]').text()).toContain('12.34')
    expect(wrapper.text()).toContain('resetSuccess')
    expect(wrapper.emitted('quota-reset')).toEqual([[result]])
    // 父列表以同一账号的新投影回写时，不能清除本次消费结果提示。
    await wrapper.setProps({ account: result.account! })
    expect(wrapper.text()).toContain('resetSuccess')
  })

  it.each(['reset', 'success', 'ok', ' OK '])('仅明确成功码 %s 和正窗口数显示成功', async (code) => {
    vi.mocked(resetOpenAIQuota).mockResolvedValue(resetResult({ code }))
    const wrapper = mountCell()
    await query(wrapper)
    await confirm(wrapper)
    expect(wrapper.text()).toContain('resetSuccess')
    expect(wrapper.emitted('quota-reset')).toHaveLength(1)
  })

  it.each([
    ['no_credit', 0, 'noCreditsAvailable'],
    ['unknown', 2, 'resetUnknown'],
    ['already_redeemed', 2, 'resetUnknown'],
    ['reset', 0, 'resetUnknown'],
  ])('HTTP 200 的 %s/%s 不冒充消费成功', async (code, windows, message) => {
    vi.mocked(resetOpenAIQuota).mockResolvedValue(resetResult({ code: String(code), windows_reset: Number(windows) }))
    const wrapper = mountCell()
    await query(wrapper)
    await confirm(wrapper)
    expect(wrapper.text()).toContain(message)
    expect(wrapper.text()).not.toContain('resetSuccess')
    expect(wrapper.emitted('quota-reset')).toBeUndefined()
    expect(wrapper.get('[data-testid="reset-quota"]').attributes('disabled')).toBeDefined()
    expect(resetOpenAIQuota).toHaveBeenCalledTimes(1)
  })

  it('消费成功但快照刷新失败时保留成功提示并清除旧次数和到期明细', async () => {
    vi.mocked(resetOpenAIQuota).mockResolvedValue(resetResult({
      cache_refreshed: false, quota: null, warning_code: 'reset_credit_cache_refresh_failed',
    }))
    const wrapper = mountCell()
    await query(wrapper)
    await confirm(wrapper)
    expect(wrapper.text()).toContain('resetSuccess')
    expect(wrapper.text()).toContain('resetCacheRefreshFailed')
    expect(wrapper.text()).not.toContain('expiresAt')
    expect(wrapper.get('[data-testid="reset-credit-query"]').text()).not.toMatch(/\d/)
    expect(wrapper.get('[data-testid="reset-quota"]').attributes('disabled')).toBeDefined()
    expect(wrapper.emitted('quota-reset')).toHaveLength(1)
    expect(refreshOpenAIQuota).toHaveBeenCalledTimes(1)
  })

  it.each([
    ['account_state_recovery_failed', 'resetAccountRecoveryFailed'],
    ['account_state_refresh_failed', 'resetAccountRefreshFailed'],
  ] as const)('账号后处理失败 %s 不覆盖重置成功', async (warningCode, message) => {
    vi.mocked(resetOpenAIQuota).mockResolvedValue(resetResult({ warning_code: warningCode }))
    const wrapper = mountCell()
    await query(wrapper)
    await confirm(wrapper)
    expect(wrapper.text()).toContain('resetSuccess')
    expect(wrapper.text()).toContain(message)
    expect(wrapper.emitted('quota-reset')).toHaveLength(1)
  })

  it('网络结果不明时禁止直接再次消费且不自动重试', async () => {
    vi.mocked(resetOpenAIQuota).mockRejectedValue({ message: 'Network Error' })
    const wrapper = mountCell()
    await query(wrapper)
    await confirm(wrapper)
    expect(wrapper.text()).toContain('resetUnknown')
    expect(wrapper.text()).not.toContain('resetSuccess')
    expect(wrapper.get('[data-testid="reset-quota"]').attributes('disabled')).toBeDefined()
    expect(resetOpenAIQuota).toHaveBeenCalledTimes(1)
    expect(refreshOpenAIQuota).toHaveBeenCalledTimes(1)
  })

  it('查询失败后不能用上一轮次数继续消费', async () => {
    const wrapper = mountCell()
    await query(wrapper)
    vi.mocked(refreshOpenAIQuota).mockRejectedValue({ message: 'query failed' })
    await query(wrapper)
    expect(wrapper.text()).toContain('query failed')
    expect(wrapper.get('[data-testid="reset-quota"]').attributes('disabled')).toBeDefined()
  })

  it('切换账号会关闭尚未提交的确认对话框', async () => {
    const wrapper = mountCell()
    await query(wrapper)
    await wrapper.get('[data-testid="reset-quota"]').trigger('click')
    await wrapper.setProps({ account: makeAccount({ id: 2 }) })
    expect(wrapper.getComponent(ConfirmDialog).props('show')).toBe(false)
    wrapper.getComponent(ConfirmDialog).vm.$emit('confirm')
    expect(resetOpenAIQuota).not.toHaveBeenCalled()
  })

  it('查询中的账号切换后再切回也不接收旧结果', async () => {
    const pending = deferred<OpenAIQuotaRefreshResult>()
    vi.mocked(refreshOpenAIQuota).mockReturnValueOnce(pending.promise)
    const wrapper = mountCell()
    await wrapper.get('[data-testid="reset-credit-query"]').trigger('click')
    await wrapper.setProps({ account: makeAccount({ id: 2 }) })
    await wrapper.setProps({ account: makeAccount() })
    pending.resolve(liveQuota)
    await flushPromises()
    expect(wrapper.get('[data-testid="reset-quota"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="reset-credit-query"]').text()).not.toMatch(/\d/)
  })

  it.each(['switch', 'unmount'])('重置响应在 %s 后不能更新新账号或通知父组件', async (action) => {
    const pending = deferred<OpenAIQuotaResetResult>()
    vi.mocked(resetOpenAIQuota).mockReturnValue(pending.promise)
    const wrapper = mountCell()
    await query(wrapper)
    await confirm(wrapper)
    if (action === 'unmount') wrapper.unmount()
    else await wrapper.setProps({ account: makeAccount({ id: 2 }) })
    pending.resolve(resetResult())
    await flushPromises()
    expect(wrapper.emitted('quota-reset')).toBeUndefined()
    if (action === 'switch') expect(wrapper.text()).not.toContain('resetSuccess')
  })
})
