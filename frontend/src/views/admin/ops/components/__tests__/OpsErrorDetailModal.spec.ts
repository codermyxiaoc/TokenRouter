import { flushPromises, shallowMount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import OpsErrorDetailModal from '../OpsErrorDetailModal.vue'

const mocks = vi.hoisted(() => ({
  getRequestErrorDetail: vi.fn(),
  listRequestErrorUpstreamErrors: vi.fn()
}))

vi.mock('@/api/admin/ops', () => ({
  opsAPI: {
    getRequestErrorDetail: mocks.getRequestErrorDetail,
    getUpstreamErrorDetail: vi.fn(),
    listRequestErrorUpstreamErrors: mocks.listRequestErrorUpstreamErrors
  }
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({ showError: vi.fn() })
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

describe('OpsErrorDetailModal', () => {
  beforeEach(() => {
    mocks.getRequestErrorDetail.mockReset()
    mocks.listRequestErrorUpstreamErrors.mockReset()
    mocks.listRequestErrorUpstreamErrors.mockResolvedValue({ items: [] })
  })

  it('prioritizes upstream root cause and deduplicates diagnostic payloads', async () => {
    mocks.getRequestErrorDetail.mockResolvedValue({
      id: 1,
      created_at: '2026-08-19T00:00:00Z',
      phase: 'request',
      type: 'upstream_error',
      error_owner: 'provider',
      error_source: 'gateway',
      severity: 'P1',
      status_code: 502,
      upstream_status_code: 429,
      platform: 'openai',
      model: 'gpt-5.6',
      resolved: false,
      request_id: 'rid-1',
      message: 'All available accounts exhausted',
      error_body: '{"error":"same"}',
      upstream_error_message: 'provider rate limit exhausted',
      upstream_error_detail: '{"error":"same"}',
      upstream_errors: '[]',
      account_name: 'account',
      group_name: 'group',
      is_business_limited: false
    })

    const wrapper = shallowMount(OpsErrorDetailModal, {
      props: { show: true, errorId: 1, errorType: 'request' },
      global: {
        stubs: {
          BaseDialog: { template: '<div><slot /></div>' },
          Icon: true
        }
      }
    })
    await flushPromises()

    expect(wrapper.text()).toContain('provider rate limit exhausted')
    expect(wrapper.text()).toContain('admin.ops.errorDetail.upstreamStatus')
    expect(wrapper.text()).toContain('429')
    expect(wrapper.findAll('pre')).toHaveLength(2)
    expect(wrapper.text()).not.toContain('admin.ops.errorDetail.payloads.upstream_detail')
  })

  // 详情保留原始上游状态，同时说明后台恢复后的最终 HTTP 状态。
  it('shows a recovered upstream error separately from the final request result', async () => {
    mocks.getRequestErrorDetail.mockResolvedValue({
      id: 2,
      created_at: '2026-09-13T02:24:11Z',
      phase: 'upstream',
      type: 'upstream_error',
      error_owner: 'provider',
      severity: 'P1',
      status_code: 503,
      upstream_status_code: 503,
      client_status_code: 200,
      recovered_upstream: true,
      group_name: '失败分组',
      recovered_group_id: 3,
      recovered_group_name: '恢复分组',
      billing_subscriptions: [{ subscription_id: 9, plan_name: '恢复后扣费套餐', amount_usd: 0.1 }],
      platform: 'openai',
      model: 'gpt-6-astra',
      request_id: 'recovered-request',
      message: 'Recovered upstream error 503: Service temporarily unavailable',
      error_body: '',
      upstream_errors: '[]',
    })

    const wrapper = shallowMount(OpsErrorDetailModal, {
      props: { show: true, errorId: 2, errorType: 'request' },
      global: { stubs: { BaseDialog: { template: '<div><slot /></div>' }, Icon: true, ErrorRecoveryStatus: false, GroupBadge: false, BillingSubscriptionSummary: false } },
    })
    await flushPromises()

    expect(wrapper.text()).toContain('503')
    expect(wrapper.text()).toContain('usage.errors.recovered')
    expect(wrapper.text()).toContain('usage.errors.finalStatus 200')
    expect(wrapper.text()).toContain('失败分组')
    expect(wrapper.text()).toContain('usage.errors.recoveredTo')
    expect(wrapper.text()).toContain('恢复分组')
    expect(wrapper.text()).toContain('恢复后扣费套餐')
  })
})
