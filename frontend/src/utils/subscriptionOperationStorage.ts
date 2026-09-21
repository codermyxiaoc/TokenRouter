// 待确认的订阅操作仅保存在当前标签页，并按管理员身份隔离，避免刷新后误造新请求。
const storageKey = (kind: string): string | null => {
  const user = JSON.parse(localStorage.getItem('auth_user') || 'null') as { id?: unknown } | null
  return typeof user?.id === 'number' && Number.isSafeInteger(user.id) && user.id > 0
    ? `subscription-operation:${user.id}:${kind}`
    : null
}

export function readPendingSubscriptionOperation<T>(kind: string): T | null {
  try {
    const key = storageKey(kind)
    const raw = key ? sessionStorage.getItem(key) : null
    return raw ? JSON.parse(raw) as T : null
  } catch {
    // 浏览器禁用存储或旧值损坏时保留页面内保护，由调用方校验恢复数据形状。
    return null
  }
}

export function writePendingSubscriptionOperation(kind: string, value: unknown | null): void {
  try {
    const key = storageKey(kind)
    if (!key) return
    if (value === null) sessionStorage.removeItem(key)
    else sessionStorage.setItem(key, JSON.stringify(value))
  } catch {
    // 存储异常不能触发重新分配，也不能中断已有请求的结果确认。
  }
}
