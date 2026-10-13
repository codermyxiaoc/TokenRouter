import type {
  DailyActivityClaimInput,
  DailyActivityType,
} from '@/api/dailyActivities'

// 按用户及活动隔离未确认请求；网络超时、刷新页面后仍用原请求号重试。
const prefix = 'daily-activity-claim:'
export function pendingClaimStorageKey(
  userId: number,
  type: DailyActivityType,
) {
  return `${prefix}${userId}:${type}`
}
export function readPendingClaim(
  userId: number,
  type: DailyActivityType,
): DailyActivityClaimInput | undefined {
  try {
    const value = JSON.parse(
      sessionStorage.getItem(pendingClaimStorageKey(userId, type)) || 'null',
    )
    if (
      value?.activity_type === type &&
      typeof value.all === 'boolean' &&
      typeof value.idempotency_key === 'string' &&
      /^[0-9a-f-]{36}$/i.test(value.idempotency_key)
    )
      return value
  } catch {
    /* 存储受限或旧数据损坏时由当前页面内存继续维护。 */
  }
  return undefined
}
export function storePendingClaim(
  userId: number,
  input: DailyActivityClaimInput,
) {
  try {
    sessionStorage.setItem(
      pendingClaimStorageKey(userId, input.activity_type),
      JSON.stringify(input),
    )
    return true
  } catch {
    // 无法持久化请求号时阻止发送，避免刷新后失去原领取请求。
    return false
  }
}
export function clearPendingClaim(userId: number, type: DailyActivityType) {
  try {
    sessionStorage.removeItem(pendingClaimStorageKey(userId, type))
  } catch {
    /* 不影响已确认的入账。 */
  }
}
export function createClaimKey(): string {
  if (typeof crypto.randomUUID === 'function') return crypto.randomUUID()
  // 非 HTTPS 开发环境也使用密码学随机数，不以时间戳代替幂等请求号。
  const bytes = crypto.getRandomValues(new Uint8Array(16))
  bytes[6] = (bytes[6]! & 15) | 64
  bytes[8] = (bytes[8]! & 63) | 128
  const hex = Array.from(bytes, (byte) =>
    byte.toString(16).padStart(2, '0'),
  ).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}
