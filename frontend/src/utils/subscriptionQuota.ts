import type { PublicSettings, UserSubscription } from '@/types'

const ONE_DAY_MS = 24 * 60 * 60 * 1000

export type SubscriptionQuotaPeriod = 'daily' | 'weekly' | 'monthly'
type QuotaSubscription = Pick<UserSubscription, 'starts_at' | 'expires_at'>
type QuotaTimezone = Pick<PublicSettings, 'server_timezone' | 'server_utc_offset'>
const calendarFormatters = new Map<string, Intl.DateTimeFormat>()

// 复用公共设置中的项目时区；旧缓存的偏移是兼容兜底，不使用浏览器本地时区。
function quotaTimezone(settings?: QuotaTimezone): Intl.DateTimeFormat | number {
  const config = settings ?? (typeof window === 'undefined' ? undefined : window.__APP_CONFIG__)
  if (config?.server_timezone) {
    try {
      let formatter = calendarFormatters.get(config.server_timezone)
      if (!formatter) {
        formatter = new Intl.DateTimeFormat('en-CA', {
          timeZone: config.server_timezone, year: 'numeric', month: '2-digit', day: '2-digit'
        })
        calendarFormatters.set(config.server_timezone, formatter)
      }
      return formatter
    } catch {
      // 无效的旧时区名称改用服务端公布的 UTC 偏移。
    }
  }
  const offset = config?.server_utc_offset?.match(/^([+-])(\d{2}):(\d{2})$/)
  if (offset && Number(offset[2]) <= 14 && Number(offset[3]) < 60) {
    return (offset[1] === '-' ? -1 : 1) * (Number(offset[2]) * 60 + Number(offset[3])) * 60_000
  }
  // 与未配置后端时区时的 Asia/Shanghai 默认值一致。
  return quotaTimezone({ server_timezone: 'Asia/Shanghai' })
}

function calendarDay(timestamp: number, formatter: Intl.DateTimeFormat): number {
  const parts = formatter.formatToParts(timestamp)
  const part = (name: string) => Number(parts.find(value => value.type === name)?.value)
  return Date.UTC(part('year'), part('month') - 1, part('day'))
}

// 在项目日历中寻找零点，按日期边界定位可兼容夏令时的 23/25 小时日。
function quotaDayStart(timestamp: number, dayOffset: number, settings?: QuotaTimezone): number {
  const timezone = quotaTimezone(settings)
  if (typeof timezone === 'number') {
    return (Math.floor((timestamp + timezone) / ONE_DAY_MS) + dayOffset) * ONE_DAY_MS - timezone
  }
  const targetDay = calendarDay(timestamp, timezone) + dayOffset * ONE_DAY_MS
  let lower = targetDay - 2 * ONE_DAY_MS
  let upper = targetDay + 2 * ONE_DAY_MS
  while (lower < upper) {
    const middle = Math.floor((lower + upper) / 2)
    if (calendarDay(middle, timezone) < targetDay) lower = middle + 1
    else upper = middle
  }
  return lower
}

// 两类订阅页面共享后端重置时间规则；只读取锚点，不推算或改写额度与重置次数。
// @project-doc docs/domains/payments_and_entitlements.md#subscription_quota_windows
export function getSubscriptionQuotaResetTime(
  subscription: QuotaSubscription,
  windowStart: string | null,
  period: SubscriptionQuotaPeriod,
  settings?: QuotaTimezone
): Date | null {
  if (!windowStart) return null
  let anchor = new Date(windowStart).getTime()
  if (!Number.isFinite(anchor)) return null
  if (period === 'daily') return new Date(quotaDayStart(anchor, 1, settings))

  const startsAt = new Date(subscription.starts_at).getTime()
  // 仅修正最初激活时被截断到生效当天零点的历史锚点，后续手动重置零点保持权威。
  if (Number.isFinite(startsAt) && anchor < startsAt && anchor === quotaDayStart(startsAt, 0, settings)) {
    anchor = startsAt
  }
  return new Date(anchor + (period === 'weekly' ? 7 : 30) * ONE_DAY_MS)
}

// 下一次重置早于订阅到期即有效，不再要求尾段容纳另一个完整周期或配置外层额度。
export function isQuotaWindowEndingAtSubscriptionExpiry(
  subscription: QuotaSubscription,
  windowStart: string | null,
  period: SubscriptionQuotaPeriod,
  settings?: QuotaTimezone
): boolean {
  const nextReset = getSubscriptionQuotaResetTime(subscription, windowStart, period, settings)
  const expiresAt = new Date(subscription.expires_at).getTime()
  if (!nextReset || !Number.isFinite(expiresAt)) return false
  if (period === 'daily' && isOneTimeDailyQuota(subscription)) return true
  return nextReset.getTime() >= expiresAt
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
