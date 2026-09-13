export const ADMIN_UI_REQUEST_HEADER = 'X-Admin-UI-Request'
export const USER_UI_REQUEST_HEADER = 'X-User-UI-Request'

function isAdminPath(path: string): boolean {
  return (
    path === '/admin' ||
    path.startsWith('/admin/') ||
    path === '/api/v1/admin' ||
    path.startsWith('/api/v1/admin/')
  )
}

function requestPath(rawURL: string): string {
  const value = rawURL.trim()
  if (!value) return ''
  try {
    const origin = typeof window !== 'undefined' ? window.location.origin : 'http://localhost'
    return new URL(value, origin).pathname
  } catch {
    return value.split(/[?#]/, 1)[0]
  }
}

/** 将 Axios 相对路径与 API 绝对路径归一化为可比较格式。 */
function normalizeAPIPath(path: string): string {
  const raw = requestPath(path)
  if (!raw) return ''
  if (raw === '/api/v1' || raw.startsWith('/api/v1/')) {
    return raw.slice('/api/v1'.length) || '/'
  }
  if (raw.startsWith('/')) {
    return raw
  }
  return `/${raw}`
}

/** 启用 ENABLE_SERVER_TIMING 后允许返回耗时信息的用户 Web API，与后端 allowlist 保持一致。 */
export function isUserTimingAPIPath(requestURL: string): boolean {
  const path = normalizeAPIPath(requestURL)
  if (!path) return false

  if (
    path === '/auth/me' ||
    path === '/auth/revoke-all-sessions' ||
    path === '/auth/oauth/bind-token'
  ) {
    return true
  }
  if (path === '/user' || path.startsWith('/user/')) return true
  if (path === '/keys' || path.startsWith('/keys/')) return true
  if (path === '/groups/available' || path === '/groups/rates') return true
  if (path === '/channels/available') return true
  if (path === '/usage' || path.startsWith('/usage/')) return true
  if (path === '/announcements' || path.startsWith('/announcements/')) return true
  if (path === '/redeem' || path.startsWith('/redeem/')) return true
  if (path === '/subscriptions' || path.startsWith('/subscriptions/')) return true
  if (path === '/channel-monitors' || path.startsWith('/channel-monitors/')) return true
  if (path.startsWith('/payment/')) {
    if (path.startsWith('/payment/public') || path.startsWith('/payment/webhook')) {
      return false
    }
    return true
  }
  return false
}

export function shouldMarkAdminUIRequest(requestURL: string, pagePath?: string): boolean {
  const currentPath =
    pagePath ?? (typeof window !== 'undefined' ? window.location.pathname : '')
  return isAdminPath(requestPath(requestURL)) || isAdminPath(currentPath)
}

export function shouldMarkUserUIRequest(requestURL: string): boolean {
  return isUserTimingAPIPath(requestURL)
}
