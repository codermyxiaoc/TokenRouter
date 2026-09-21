import { describe, expect, it } from 'vitest'
import { isQuotaWindowEndingAtSubscriptionExpiry as endsAtExpiry, type SubscriptionQuotaPeriod } from '../subscriptionQuota'

const day = 24 * 60 * 60 * 1000
const start = Date.parse('2026-09-21T00:00:00+08:00')
const at = (days: number) => new Date(start + days * day).toISOString()
const subscription = (expiryDays: number, weekly: number | null = null, monthly: number | null = null) => ({
  starts_at: at(-60), expires_at: at(expiryDays), weekly_limit_usd: weekly, monthly_limit_usd: monthly
})

describe('subscription quota tail reset display', () => {
  it('allows daily reset under weekly or monthly finite protection', () => {
    expect(endsAtExpiry(subscription(1.5, 10), at(0), 'daily')).toBe(false)
    expect(endsAtExpiry(subscription(1.5, null, 100), at(0), 'daily')).toBe(false)
    expect(endsAtExpiry(subscription(1.5), at(0), 'daily')).toBe(true)
  })

  it('allows weekly tail reset only under monthly protection', () => {
    expect(endsAtExpiry(subscription(8, 10, 100), at(0), 'weekly')).toBe(false)
    expect(endsAtExpiry(subscription(8, 10), at(0), 'weekly')).toBe(true)
    expect(endsAtExpiry(subscription(31, 10, 100), at(0), 'monthly')).toBe(true)
  })

  it.each([null, 0, -1, Number.POSITIVE_INFINITY, Number.NaN])('does not treat %s as a finite outer quota', limit => {
    expect(endsAtExpiry(subscription(1.5, limit, limit), at(0), 'daily')).toBe(true)
    expect(endsAtExpiry(subscription(8, 10, limit), at(0), 'weekly')).toBe(true)
  })

  it('preserves the one-time daily quota exception even with outer limits', () => {
    expect(endsAtExpiry({ ...subscription(0.8, 10, 100), starts_at: at(-0.1) }, at(0), 'daily')).toBe(true)
    expect(endsAtExpiry({ ...subscription(1, 10, 100), starts_at: at(0) }, at(0), 'daily')).toBe(true)
  })

  it.each([['daily', 1], ['weekly', 7], ['monthly', 30]] as [SubscriptionQuotaPeriod, number][])(
    'keeps the %s full-window and expiry boundaries', (period, days) => {
      expect(endsAtExpiry(subscription(days * 2), at(0), period)).toBe(false)
      expect(endsAtExpiry(subscription(days * 2 - 1 / 86400), at(0), period)).toBe(true)
      expect(endsAtExpiry(subscription(days, 10, 100), at(0), period)).toBe(true)
      expect(endsAtExpiry(subscription(days - 1 / 86400, 10, 100), at(0), period)).toBe(true)
    }
  )

  it('does not invent a reset for missing or invalid window data', () => {
    expect(endsAtExpiry(subscription(70), null, 'daily')).toBe(false)
    expect(endsAtExpiry(subscription(70), 'invalid', 'daily')).toBe(false)
    expect(endsAtExpiry({ ...subscription(70), expires_at: 'invalid' }, at(0), 'daily')).toBe(false)
  })
})
