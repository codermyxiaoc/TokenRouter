interface SyncErrorPayload {
  message?: unknown
  detail?: unknown
  details?: unknown
}

// 只读取后端约定的说明字段；HTML 错误页和任意对象不能成为提示正文。
function readErrorText(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined
  const text = value.trim()
  if (!text || /<\/?[a-z][^>]*>|<!doctype\b/i.test(text)) return undefined
  return text
}

export function upstreamModelSyncErrorMessage(error: unknown, fallback: string): string {
  if (!error || typeof error !== 'object') return fallback
  const normalized = error as SyncErrorPayload & { response?: { data?: unknown } }
  const responseData = normalized.response?.data
  const response = responseData && typeof responseData === 'object'
    ? responseData as SyncErrorPayload
    : undefined

  // Axios 的顶层 message 通常只有 HTTP 状态，优先保留后端安全错误说明。
  const message = readErrorText(response?.message)
    ?? readErrorText(response?.detail)
    ?? readErrorText(normalized.message)
    ?? readErrorText(normalized.detail)
  const details = readErrorText(response?.details) ?? readErrorText(normalized.details)
  if (details && !message?.includes(details)) return `${message ?? fallback}: ${details}`

  // 后端可能已经包含“同步失败”上下文，不再重复添加相同前缀。
  return message ?? fallback
}
