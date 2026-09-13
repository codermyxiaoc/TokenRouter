export const LOGIN_AGREEMENT_STORAGE_KEY = 'sub2api_login_agreement_consent'

// 登录协议按 revision 记录，条款更新后旧确认不会继续放行快捷登录。
export function hasAcceptedLoginAgreement(revision: string): boolean {
  if (!revision || typeof window === 'undefined') return false
  try {
    const raw = window.localStorage.getItem(LOGIN_AGREEMENT_STORAGE_KEY)
    if (!raw) return false
    const parsed = JSON.parse(raw) as { revision?: string }
    return parsed.revision === revision
  } catch {
    return false
  }
}
