import axios from 'axios'

// 仅网络、限流和服务端故障属于暂时不可用；明确的身份错误仍须清理会话。
export function isTemporaryAuthFailure(error: unknown): boolean {
  if (axios.isAxiosError(error)) {
    const status = error.response?.status ?? 0
    return status === 0 || status === 429 || status >= 500
  }
  const status = (error as { status?: number } | null)?.status
  return status === 0 || status === 429 || (typeof status === 'number' && status >= 500)
}
