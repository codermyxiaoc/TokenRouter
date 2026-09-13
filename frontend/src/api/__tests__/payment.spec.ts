import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: {
    get,
    post,
  },
}))

import { paymentAPI } from '@/api/payment'

describe('payment api', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
    get.mockResolvedValue({ data: {} })
    post.mockResolvedValue({ data: {} })
  })

  it('keeps legacy public out_trade_no verification for upgrade compatibility', async () => {
    await paymentAPI.verifyOrderPublic('legacy-order-no')

    expect(post).toHaveBeenCalledWith('/payment/public/orders/verify', {
      out_trade_no: 'legacy-order-no',
    })
  })

  it('keeps signed public resume-token resolve endpoint', async () => {
    await paymentAPI.resolveOrderPublicByResumeToken('resume-token-123')

    expect(post).toHaveBeenCalledWith('/payment/public/orders/resolve', {
      resume_token: 'resume-token-123',
    })
  })

  it('sends wallet retry keys in the header without leaking them into the order body', async () => {
    const payload = { amount: 10, payment_type: 'balance', order_type: 'subscription', plan_id: 7 }
    await paymentAPI.createOrder({ ...payload, idempotency_key: 'wallet-retry-key' })

    expect(post).toHaveBeenCalledWith('/payment/orders', payload, {
      headers: { 'Idempotency-Key': 'wallet-retry-key' },
    })
  })

  it('preserves external payment requests without an idempotency key', async () => {
    const payload = { amount: 10, payment_type: 'wxpay', order_type: 'balance' }
    await paymentAPI.createOrder(payload)
    expect(post).toHaveBeenCalledWith('/payment/orders', payload)
  })
})
