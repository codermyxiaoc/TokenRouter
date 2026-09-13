const OAUTH_AFFILIATE_CODE_KEY = 'oauth_aff_code'
const AFFILIATE_CODE_KEY = 'affiliate_aff_code'
const AFFILIATE_CODE_TTL_MS = 30 * 24 * 60 * 60 * 1000

interface StoredAffiliateCode {
  code: string
  expiresAt: number
}

export function normalizeOAuthAffiliateCode(value?: unknown): string {
  const raw = Array.isArray(value) ? value[0] : value
  return typeof raw === 'string' ? raw.trim() : ''
}

export function pickOAuthAffiliateCode(...values: unknown[]): string {
  for (const value of values) {
    const code = normalizeOAuthAffiliateCode(value)
    if (code) {
      return code
    }
  }
  return ''
}

export function storeAffiliateCode(value?: unknown, now = Date.now()): void {
  if (typeof window === 'undefined') {
    return
  }
  const code = normalizeOAuthAffiliateCode(value)
  if (!code) {
    return
  }
  try {
    // aff 参数来自邀请链接，短期保存在本地便于注册页和 OAuth 跳转共享。
    const payload: StoredAffiliateCode = {
      code,
      expiresAt: now + AFFILIATE_CODE_TTL_MS
    }
    window.localStorage.setItem(AFFILIATE_CODE_KEY, JSON.stringify(payload))
  } catch {
    // 忽略浏览器存储异常，注册流程仍可继续。
  }
}

export function loadAffiliateCode(now = Date.now()): string {
  if (typeof window === 'undefined') {
    return ''
  }
  try {
    const raw = window.localStorage.getItem(AFFILIATE_CODE_KEY)
    if (!raw) {
      return ''
    }
    const parsed = JSON.parse(raw) as Partial<StoredAffiliateCode>
    const code = normalizeOAuthAffiliateCode(parsed.code)
    const expiresAt = Number(parsed.expiresAt) || 0
    if (!code || expiresAt <= now) {
      clearAffiliateCode()
      return ''
    }
    return code
  } catch {
    clearAffiliateCode()
    return ''
  }
}

export function clearAffiliateCode(): void {
  if (typeof window === 'undefined') {
    return
  }
  try {
    window.localStorage.removeItem(AFFILIATE_CODE_KEY)
  } catch {
    // 忽略浏览器存储异常。
  }
}

export function resolveAffiliateCode(...values: unknown[]): string {
  const code = pickOAuthAffiliateCode(...values)
  if (code) {
    storeAffiliateCode(code)
    return code
  }
  return loadAffiliateCode()
}

export function storeOAuthAffiliateCode(value?: unknown): void {
  if (typeof window === 'undefined') {
    return
  }
  const code = normalizeOAuthAffiliateCode(value)
  try {
    if (code) {
      window.sessionStorage.setItem(OAUTH_AFFILIATE_CODE_KEY, code)
    } else {
      window.sessionStorage.removeItem(OAUTH_AFFILIATE_CODE_KEY)
    }
  } catch {
    // 忽略浏览器存储异常。
  }
}

export function loadOAuthAffiliateCode(): string {
  if (typeof window === 'undefined') {
    return ''
  }
  try {
    return normalizeOAuthAffiliateCode(window.sessionStorage.getItem(OAUTH_AFFILIATE_CODE_KEY))
  } catch {
    return ''
  }
}

export function clearOAuthAffiliateCode(): void {
  if (typeof window === 'undefined') {
    return
  }
  try {
    window.sessionStorage.removeItem(OAUTH_AFFILIATE_CODE_KEY)
  } catch {
    // 忽略浏览器存储异常。
  }
}

export function oauthAffiliatePayload(code: string): { aff_code?: string } {
  const normalized = normalizeOAuthAffiliateCode(code)
  return normalized ? { aff_code: normalized } : {}
}

export function clearAllAffiliateCodes(): void {
  clearOAuthAffiliateCode()
  clearAffiliateCode()
}
