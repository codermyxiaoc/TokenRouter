import type { UserSubscription } from '@/types'

const ONE_DAY_MS = 24 * 60 * 60 * 1000

export type SubscriptionQuotaPeriod = 'daily' | 'weekly' | 'monthly'

// 与后端窗口规则一致：有限外层额度允许日/周尾段刷新；无限额度不构成保护层。
// @project-doc docs/domains/payments_and_entitlements.md#subscription_quota_windows
export function isQuotaWindowEndingAtSubscriptionExpiry(
  subscription: Pick<UserSubscription, 'starts_at' | 'expires_at' | 'weekly_limit_usd' | 'monthly_limit_usd'>,
  windowStart: string | null,
  period: SubscriptionQuotaPeriod
): boolean {
  if (!windowStart) return false
  const start = new Date(windowStart).getTime()
  const expiresAt = new Date(subscription.expires_at).getTime()
  if (!Number.isFinite(start) || !Number.isFinite(expiresAt)) return false
  if (period === 'daily' && isOneTimeDailyQuota(subscription)) return true

  const days = { daily: 1, weekly: 7, monthly: 30 }[period]
  const windowMs = days * ONE_DAY_MS
  const nextWindowStart = start + windowMs
  // 已到订阅有效期终点时，即使存在外层额度也不能展示下一次刷新。
  if (nextWindowStart >= expiresAt) return true
  if (nextWindowStart + windowMs <= expiresAt) return false

  const positiveFiniteLimit = (value: number | null) => value != null && Number.isFinite(value) && value > 0
  if (period === 'daily' && positiveFiniteLimit(subscription.weekly_limit_usd)) return false
  if (period !== 'monthly' && positiveFiniteLimit(subscription.monthly_limit_usd)) return false
  return true
}

// ExpirationDateRelation 表示到期时间与当前本地日历日期的关系。
export type ExpirationDateRelation = 'expired' | 'today' | 'tomorrow' | 'later'

// RemainingExpiryDuration 提供适合界面展示的到期剩余时长精度。
export type RemainingExpiryDuration =
  | { unit: 'days'; days: number }
  | { unit: 'hoursMinutes'; hours: number; minutes: number }

export interface RemainingDurationParts {
  days: number
  hours: number
  minutes: number
}

export function isOneTimeDailyQuota(
  subscription: Pick<UserSubscription, 'starts_at' | 'expires_at'>
): boolean {
  if (!subscription.starts_at || !subscription.expires_at) return false

  const startsAt = new Date(subscription.starts_at).getTime()
  const expiresAt = new Date(subscription.expires_at).getTime()

  if (!Number.isFinite(startsAt) || !Number.isFinite(expiresAt)) return false

  return expiresAt <= startsAt + ONE_DAY_MS
}

export function getRemainingDurationParts(
  targetAt: Date | string,
  now: Date = new Date()
): RemainingDurationParts | null {
  const targetTime = targetAt instanceof Date ? targetAt.getTime() : new Date(targetAt).getTime()
  const nowTime = now.getTime()

  if (!Number.isFinite(targetTime) || !Number.isFinite(nowTime)) return null

  const diffMs = targetTime - nowTime
  if (diffMs <= 0) return null

  const totalMinutes = Math.floor(diffMs / (1000 * 60))
  const days = Math.floor(totalMinutes / (24 * 60))
  const hours = Math.floor((totalMinutes % (24 * 60)) / 60)
  const minutes = totalMinutes % 60

  return { days, hours, minutes }
}

// getExpirationDateRelation 按本地日历日判断今天、明天和更晚日期。
export function getExpirationDateRelation(
  targetAt: Date | string,
  now: Date = new Date()
): ExpirationDateRelation | null {
  const target = targetAt instanceof Date ? targetAt : new Date(targetAt)
  const targetTime = target.getTime()
  const nowTime = now.getTime()

  if (!Number.isFinite(targetTime) || !Number.isFinite(nowTime)) return null
  if (targetTime <= nowTime) return 'expired'

  const targetDay = Date.UTC(target.getFullYear(), target.getMonth(), target.getDate())
  const currentDay = Date.UTC(now.getFullYear(), now.getMonth(), now.getDate())
  const calendarDays = Math.round((targetDay - currentDay) / ONE_DAY_MS)

  if (calendarDays === 0) return 'today'
  if (calendarDays === 1) return 'tomorrow'
  return 'later'
}

// getRemainingExpiryDuration 在不足一天时保留小时和分钟，否则向上取整到天。
export function getRemainingExpiryDuration(
  targetAt: Date | string,
  now: Date = new Date()
): RemainingExpiryDuration | null {
  const targetTime = targetAt instanceof Date ? targetAt.getTime() : new Date(targetAt).getTime()
  const nowTime = now.getTime()

  if (!Number.isFinite(targetTime) || !Number.isFinite(nowTime)) return null

  const diffMs = targetTime - nowTime
  if (diffMs <= 0) return null
  if (diffMs >= ONE_DAY_MS) {
    return { unit: 'days', days: Math.ceil(diffMs / ONE_DAY_MS) }
  }

  const totalMinutes = Math.ceil(diffMs / (60 * 1000))
  return {
    unit: 'hoursMinutes',
    hours: Math.floor(totalMinutes / 60),
    minutes: totalMinutes % 60
  }
}

// highestQuotaExhausted 按服务端规则只检查最高层正数额度，避免低层窗口暂时耗尽时误导用户撤销套餐。
export function highestQuotaExhausted(subscription: Pick<
  UserSubscription,
  'monthly_limit_usd' | 'monthly_usage_usd' | 'weekly_limit_usd' | 'weekly_usage_usd' | 'daily_limit_usd' | 'daily_usage_usd'
>): boolean {
  const windows = [
    { limit: subscription.monthly_limit_usd, used: subscription.monthly_usage_usd },
    { limit: subscription.weekly_limit_usd, used: subscription.weekly_usage_usd },
    { limit: subscription.daily_limit_usd, used: subscription.daily_usage_usd }
  ]
  const highest = windows.find(
    (window) => window.limit != null && Number.isFinite(window.limit) && window.limit > 0
  )
  return highest != null && highest.limit != null && highest.used >= highest.limit
}
