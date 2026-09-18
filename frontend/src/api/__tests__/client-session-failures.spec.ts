import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import axios, { type AxiosInstance } from 'axios'

vi.mock('@/i18n', () => ({ getLocale: () => 'zh-CN' }))

describe('认证服务暂时故障', () => {
  let client: AxiosInstance
  beforeEach(async () => {
    vi.resetModules()
    localStorage.clear()
    sessionStorage.clear()
    window.history.replaceState({}, '', '/')
    Object.defineProperty(navigator, 'locks', { configurable: true, value: { request: (_name: string, fn: () => unknown) => fn() } })
    client = (await import('@/api/client')).apiClient
    localStorage.setItem('auth_token', 'expired-token')
    localStorage.setItem('refresh_token', 'refresh-token')
    localStorage.setItem('auth_user', JSON.stringify({ id: 7 }))
    localStorage.setItem('token_expires_at', String(Date.now() - 1))
  })
  afterEach(() => vi.restoreAllMocks())

  const unauthorized = () => ({ response: { status: 401, data: { code: 'TOKEN_EXPIRED' } }, config: { url: '/private', headers: { Authorization: 'Bearer expired-token' } } })

  it.each([0, 429, 500, 503])('刷新失败 %i 保留凭据且恢复后可重试', async (status) => {
    const adapter = vi.fn().mockRejectedValueOnce(unauthorized()).mockRejectedValueOnce(unauthorized()).mockResolvedValueOnce({ status: 200, data: { code: 0, data: { ok: true } }, headers: {}, config: {} })
    client.defaults.adapter = adapter
    vi.spyOn(axios, 'post').mockRejectedValueOnce({ isAxiosError: true, message: 'Temporary failure', response: status ? { status, data: {} } : undefined }).mockResolvedValueOnce({ data: { code: 0, data: { access_token: 'new-access', refresh_token: 'new-refresh', expires_in: 3600, token_type: 'Bearer' } } })
    await expect(client.get('/private')).rejects.toMatchObject({ status, code: 'TOKEN_REFRESH_UNAVAILABLE' })
    expect(localStorage.getItem('refresh_token')).toBe('refresh-token')
    expect(localStorage.getItem('auth_token')).toBe('expired-token')
    expect(localStorage.getItem('auth_user')).toBe(JSON.stringify({ id: 7 }))
    expect(sessionStorage.getItem('auth_expired')).toBeNull()
    expect(window.location.pathname).toBe('/')
    expect(adapter).toHaveBeenCalledTimes(1)
    await expect(client.get('/private')).resolves.toMatchObject({ data: { ok: true } })
    expect(localStorage.getItem('refresh_token')).toBe('new-refresh')
  })

  it.each([400, 401, 403])('明确刷新身份失败 %i 仍清理失效会话', async (status) => {
    window.history.replaceState({}, '', '/login')
    client.defaults.adapter = vi.fn().mockRejectedValue(unauthorized())
    vi.spyOn(axios, 'post').mockRejectedValueOnce({ isAxiosError: true, response: { status, data: {} } })
    await expect(client.get('/private')).rejects.toMatchObject({ code: 'TOKEN_REFRESH_FAILED' })
    expect(localStorage.getItem('refresh_token')).toBeNull()
    expect(localStorage.getItem('auth_token')).toBeNull()
    expect(localStorage.getItem('auth_user')).toBeNull()
  })
})
