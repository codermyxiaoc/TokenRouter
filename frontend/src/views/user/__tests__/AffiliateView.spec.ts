import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { UserAffiliateDetail } from '@/types'
import AffiliateView from '../AffiliateView.vue'

const {
  getAffiliateDetail,
  transferAffiliateQuota,
  showError,
  showSuccess,
  refreshUser,
  copyToClipboard,
  formatBalanceAmount,
} = vi.hoisted(() => ({
  getAffiliateDetail: vi.fn(),
  transferAffiliateQuota: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
  refreshUser: vi.fn(),
  copyToClipboard: vi.fn(),
  formatBalanceAmount: vi.fn(),
}))

vi.mock('@/api/user', () => ({
  default: {
    getAffiliateDetail,
    transferAffiliateQuota,
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError,
    showSuccess,
  }),
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    refreshUser,
  }),
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({
    copyToClipboard,
  }),
}))

vi.mock('@/composables/useBalanceDisplay', () => ({
  useBalanceDisplay: () => ({
    balanceUnitName: { value: '推理积分' },
    formatBalanceAmount,
  }),
}))

vi.mock('@/utils/format', () => ({
  formatDateTime: () => '2026-07-17 12:00:00',
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params?.amount ? `${key}:${String(params.amount)}` : key,
    }),
  }
})

// 使用不同于美元的符号，确保页面确实依赖后台余额单位格式化器。
const affiliateDetail: UserAffiliateDetail = {
  user_id: 1,
  aff_code: 'INVITE20',
  inviter_id: null,
  aff_count: 1,
  aff_quota: 200,
  aff_frozen_quota: 25,
  aff_history_quota: 325,
  effective_rebate_rate_percent: 20,
  invitees: [
    {
      user_id: 2,
      email: 'i***e@example.com',
      username: 'invitee',
      created_at: '2026-07-17T03:00:00Z',
      total_rebate: 200,
    },
  ],
}

function mountView() {
  return mount(AffiliateView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        Icon: true,
      },
    },
  })
}

describe('AffiliateView', () => {
  beforeEach(() => {
    getAffiliateDetail.mockReset()
    transferAffiliateQuota.mockReset()
    showError.mockReset()
    showSuccess.mockReset()
    refreshUser.mockReset()
    copyToClipboard.mockReset()
    formatBalanceAmount.mockReset()

    getAffiliateDetail.mockResolvedValue(affiliateDetail)
    transferAffiliateQuota.mockResolvedValue({ transferred_quota: 200, balance: 500 })
    refreshUser.mockResolvedValue(undefined)
    formatBalanceAmount.mockImplementation(
      (value: number | null | undefined) => `积分${Number(value ?? 0).toFixed(2)}`,
    )
  })

  it('uses the configured balance unit for every affiliate amount', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.text()).toContain('积分200.00')
    expect(wrapper.text()).toContain('积分325.00')
    expect(wrapper.text()).toContain('积分25.00')
    expect(wrapper.text()).not.toContain('US$')
    expect(formatBalanceAmount).toHaveBeenCalledWith(affiliateDetail.aff_quota)
    expect(formatBalanceAmount).toHaveBeenCalledWith(affiliateDetail.aff_history_quota)
    expect(formatBalanceAmount).toHaveBeenCalledWith(affiliateDetail.aff_frozen_quota)
    expect(formatBalanceAmount).toHaveBeenCalledWith(affiliateDetail.invitees[0].total_rebate)
  })

  it('uses the configured balance unit in the transfer success message', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()

    expect(transferAffiliateQuota).toHaveBeenCalledOnce()
    expect(showSuccess).toHaveBeenCalledWith('affiliate.transfer.success:积分200.00')
  })

  it('stacks long invite values and copy buttons on mobile while retaining desktop rows', async () => {
    const longCode = 'INVITE-' + 'X'.repeat(120)
    getAffiliateDetail.mockResolvedValueOnce({ ...affiliateDetail, aff_code: longCode })
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-testid="affiliate-code-row"]').classes()).toEqual(
      expect.arrayContaining(['min-w-0', 'flex-col', 'items-stretch', 'sm:flex-row', 'sm:items-center']),
    )
    expect(wrapper.get('[data-testid="affiliate-link-row"]').classes()).toEqual(
      expect.arrayContaining(['min-w-0', 'flex-col', 'items-stretch', 'sm:flex-row', 'sm:items-center']),
    )
    expect(wrapper.get('[data-testid="affiliate-code"]').classes()).toEqual(
      expect.arrayContaining(['min-w-0', 'break-all', 'sm:flex-1', 'sm:truncate']),
    )
    expect(wrapper.get('[data-testid="affiliate-link"]').classes()).toEqual(
      expect.arrayContaining(['min-w-0', 'break-all', 'sm:flex-1', 'sm:truncate']),
    )
    expect(wrapper.get('[data-testid="affiliate-copy-code"]').classes()).toEqual(
      expect.arrayContaining(['w-full', 'sm:w-auto', 'sm:shrink-0']),
    )
    expect(wrapper.get('[data-testid="affiliate-copy-link"]').classes()).toEqual(
      expect.arrayContaining(['w-full', 'sm:w-auto', 'sm:shrink-0']),
    )

    await wrapper.get('[data-testid="affiliate-copy-code"]').trigger('click')
    await wrapper.get('[data-testid="affiliate-copy-link"]').trigger('click')

    expect(copyToClipboard).toHaveBeenNthCalledWith(1, longCode, 'affiliate.codeCopied')
    expect(copyToClipboard).toHaveBeenNthCalledWith(
      2,
      `${window.location.origin}/register?aff=${encodeURIComponent(longCode)}`,
      'affiliate.linkCopied',
    )
  })
})
