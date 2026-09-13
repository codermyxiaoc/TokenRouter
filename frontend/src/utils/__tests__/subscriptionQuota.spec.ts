import { describe, expect, it } from 'vitest'

import { getExpirationDateRelation, getRemainingExpiryDuration, highestQuotaExhausted } from '../subscriptionQuota'

describe('subscription expiry timing', () => {
  it('uses local calendar dates for today and tomorrow', () => {
    const now = new Date(2026, 2, 7, 23, 30)

    expect(getExpirationDateRelation(new Date(2026, 2, 7, 23, 45), now)).toBe('today')
    expect(getExpirationDateRelation(new Date(2026, 2, 8, 3, 30), now)).toBe('tomorrow')
  })

  it('treats the exact expiry instant and elapsed expiries as expired', () => {
    const now = new Date(2026, 6, 30, 9, 0)

    expect(getExpirationDateRelation(now, now)).toBe('expired')
    expect(getRemainingExpiryDuration(now, now)).toBeNull()
    expect(getExpirationDateRelation(new Date(2026, 6, 30, 8, 59), now)).toBe('expired')
    expect(getRemainingExpiryDuration(new Date(2026, 6, 30, 8, 59), now)).toBeNull()
  })

  it('rejects invalid target and current dates', () => {
    const invalid = new Date('invalid')
    const valid = new Date(2026, 6, 30, 9, 0)

    expect(getExpirationDateRelation(invalid, valid)).toBeNull()
    expect(getExpirationDateRelation(valid, invalid)).toBeNull()
    expect(getRemainingExpiryDuration(invalid, valid)).toBeNull()
    expect(getRemainingExpiryDuration(valid, invalid)).toBeNull()
  })

  it('returns rounded-up hours and minutes for an expiry under 24 hours away', () => {
    const now = new Date(2026, 6, 30, 9, 0)

    expect(getRemainingExpiryDuration(new Date(2026, 6, 31, 8, 30), now)).toEqual({
      unit: 'hoursMinutes',
      hours: 23,
      minutes: 30
    })
    expect(getRemainingExpiryDuration(new Date(now.getTime() + 1), now)).toEqual({
      unit: 'hoursMinutes',
      hours: 0,
      minutes: 1
    })
    expect(getRemainingExpiryDuration(new Date(now.getTime() + 23 * 60 * 60 * 1000 + 1), now)).toEqual({
      unit: 'hoursMinutes',
      hours: 23,
      minutes: 1
    })
  })

  it('preserves rounded-up day display from 24 hours onward', () => {
    const now = new Date(2026, 6, 30, 9, 0)

    expect(getRemainingExpiryDuration(new Date(now.getTime() + 24 * 60 * 60 * 1000), now)).toEqual({
      unit: 'days',
      days: 1
    })
    expect(getRemainingExpiryDuration(new Date(now.getTime() + 24 * 60 * 60 * 1000 + 1), now)).toEqual({
      unit: 'days',
      days: 2
    })
  })
})

describe('highest subscription quota exhaustion', () => {
  it('checks monthly quota before lower windows', () => {
    expect(highestQuotaExhausted({ monthly_limit_usd: 100, monthly_usage_usd: 99, weekly_limit_usd: 10, weekly_usage_usd: 10, daily_limit_usd: 1, daily_usage_usd: 1 })).toBe(false)
    expect(highestQuotaExhausted({ monthly_limit_usd: 100, monthly_usage_usd: 100, weekly_limit_usd: 10, weekly_usage_usd: 0, daily_limit_usd: 1, daily_usage_usd: 0 })).toBe(true)
  })

  it('falls back to weekly and daily quotas when higher windows are absent', () => {
    expect(highestQuotaExhausted({ monthly_limit_usd: null, monthly_usage_usd: 100, weekly_limit_usd: 10, weekly_usage_usd: 10, daily_limit_usd: 1, daily_usage_usd: 0 })).toBe(true)
    expect(highestQuotaExhausted({ monthly_limit_usd: null, monthly_usage_usd: 100, weekly_limit_usd: null, weekly_usage_usd: 10, daily_limit_usd: 1, daily_usage_usd: 1 })).toBe(true)
  })

  it('does not mark unlimited or lower-only exhaustion as revocable', () => {
    expect(highestQuotaExhausted({ monthly_limit_usd: null, monthly_usage_usd: 0, weekly_limit_usd: 10, weekly_usage_usd: 9, daily_limit_usd: 1, daily_usage_usd: 1 })).toBe(false)
    expect(highestQuotaExhausted({ monthly_limit_usd: 0, monthly_usage_usd: 10, weekly_limit_usd: 0, weekly_usage_usd: 10, daily_limit_usd: null, daily_usage_usd: 1 })).toBe(false)
    expect(highestQuotaExhausted({ monthly_limit_usd: Number.POSITIVE_INFINITY, monthly_usage_usd: Number.POSITIVE_INFINITY, weekly_limit_usd: null, weekly_usage_usd: 0, daily_limit_usd: null, daily_usage_usd: 0 })).toBe(false)
  })
})
