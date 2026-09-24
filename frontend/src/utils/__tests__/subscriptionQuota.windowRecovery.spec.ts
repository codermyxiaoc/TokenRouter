import { afterEach, describe, expect, it } from 'vitest'
import type { PublicSettings } from '@/types'
import {
  getSubscriptionQuotaResetTime as resetAt,
  isQuotaWindowEndingAtSubscriptionExpiry as endsAtExpiry,
  type SubscriptionQuotaPeriod
} from '../subscriptionQuota'

const day = 24 * 60 * 60 * 1000
const start = Date.parse('2026-09-21T00:00:00+08:00')
const at = (days: number) => new Date(start + days * day).toISOString()
const subscription = (expiryDays: number, weekly: number | null = null, monthly: number | null = null) => ({
  starts_at: at(-60), expires_at: at(expiryDays), weekly_limit_usd: weekly, monthly_limit_usd: monthly
})
const shanghai = { server_timezone: 'Asia/Shanghai' }
const originalConfig = window.__APP_CONFIG__

afterEach(() => { window.__APP_CONFIG__ = originalConfig })

describe('subscription quota tail reset display', () => {
  it.each([null, 0, -1, 10, Number.POSITIVE_INFINITY, Number.NaN])('allows tail resets regardless of outer quota %s', limit => {
    expect(endsAtExpiry(subscription(1.5, limit, limit), at(0), 'daily', shanghai)).toBe(false)
    expect(endsAtExpiry(subscription(8, limit, limit), at(0), 'weekly', shanghai)).toBe(false)
    expect(endsAtExpiry(subscription(31, limit, limit), at(0), 'monthly', shanghai)).toBe(false)
  })

  it('preserves the one-time daily quota exception even with outer limits', () => {
    expect(endsAtExpiry({ ...subscription(0.8, 10, 100), starts_at: at(-0.1) }, at(0), 'daily')).toBe(true)
    expect(endsAtExpiry({ ...subscription(1, 10, 100), starts_at: at(0) }, at(0), 'daily')).toBe(true)
  })

  it.each([['daily', 1], ['weekly', 7], ['monthly', 30]] as [SubscriptionQuotaPeriod, number][])(
    'permits %s resets strictly before expiry without requiring a second complete cycle', (period, days) => {
      expect(endsAtExpiry(subscription(days * 2 - 1 / 86400), at(0), period, shanghai)).toBe(false)
      expect(endsAtExpiry(subscription(days + 1 / day), at(0), period, shanghai)).toBe(false)
      expect(endsAtExpiry(subscription(days, 10, 100), at(0), period, shanghai)).toBe(true)
      expect(endsAtExpiry(subscription(days - 1 / day, 10, 100), at(0), period, shanghai)).toBe(true)
    }
  )

  it('shows September 30 as the weekly reset before the October 5 expiry', () => {
    const sub = { starts_at: '2026-09-05T10:18:00+08:00', expires_at: '2026-10-05T10:18:00+08:00' }
    const anchor = '2026-09-23T00:00:00+08:00'
    expect(resetAt(sub, anchor, 'weekly', shanghai)?.toISOString()).toBe('2026-09-29T16:00:00.000Z')
    expect(endsAtExpiry(sub, anchor, 'weekly', shanghai)).toBe(false)
  })

  it.each([['weekly', 7], ['monthly', 30]] as const)('corrects only the initial truncated %s anchor', (period, days) => {
    const sub = { starts_at: at(0.5), expires_at: at(100) }
    expect(resetAt(sub, at(0), period, shanghai)?.toISOString()).toBe(at(days + 0.5))
    // 后续手动重置的零点、非零点和更早的异常锚点不能被无差别改为生效时间。
    expect(resetAt(sub, at(2), period, shanghai)?.toISOString()).toBe(at(days + 2))
    expect(resetAt(sub, at(2.25), period, shanghai)?.toISOString()).toBe(at(days + 2.25))
    expect(resetAt(sub, at(-1), period, shanghai)?.toISOString()).toBe(at(days - 1))
  })

  it.each([['weekly', 7], ['monthly', 30]] as const)('preserves the exact %s anchor after multiple periods', (period, days) => {
    const sub = { starts_at: at(0.5), expires_at: at(200) }
    const anchor = at(0.5 + days * 3)
    expect(resetAt(sub, anchor, period, shanghai)?.toISOString()).toBe(at(0.5 + days * 4))
  })

  it('uses the next project midnight for a legacy non-midnight daily anchor', () => {
    expect(resetAt(subscription(70), at(1 / 3), 'daily', shanghai)?.toISOString()).toBe(at(1))
    window.__APP_CONFIG__ = { server_timezone: 'Asia/Shanghai' } as PublicSettings
    expect(resetAt(subscription(70), at(1 / 3), 'daily')?.toISOString()).toBe(at(1))
  })

  it.each([
    ['2026-03-08T00:00:00-05:00', '2026-03-09T00:00:00-04:00', 23],
    ['2026-11-01T00:00:00-04:00', '2026-11-02T00:00:00-05:00', 25]
  ])('uses project calendar boundaries across DST from %s', (anchor, next, hours) => {
    const reset = resetAt(subscription(70), String(anchor), 'daily', { server_timezone: 'America/New_York' })!
    expect(reset.getTime()).toBe(new Date(String(next)).getTime())
    expect(reset.getTime() - new Date(String(anchor)).getTime()).toBe(Number(hours) * 3_600_000)
  })

  it('uses the published UTC offset when an old settings cache has no valid time zone', () => {
    const settings = { server_timezone: 'invalid/timezone', server_utc_offset: '+05:30' }
    expect(resetAt(subscription(70), '2026-09-23T12:00:00Z', 'daily', settings)?.toISOString()).toBe('2026-09-23T18:30:00.000Z')
    window.__APP_CONFIG__ = undefined
    expect(resetAt(subscription(70), at(1 / 3), 'daily')?.toISOString()).toBe(at(1))
  })

  it('does not invent a reset for missing or invalid window data', () => {
    expect(resetAt(subscription(70), null, 'daily', shanghai)).toBeNull()
    expect(resetAt(subscription(70), 'invalid', 'weekly', shanghai)).toBeNull()
    expect(endsAtExpiry(subscription(70), null, 'daily')).toBe(false)
    expect(endsAtExpiry(subscription(70), 'invalid', 'daily')).toBe(false)
    expect(endsAtExpiry({ ...subscription(70), expires_at: 'invalid' }, at(0), 'daily')).toBe(false)
  })
})
