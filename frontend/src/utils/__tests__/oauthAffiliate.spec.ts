import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  clearAffiliateCode,
  clearOAuthAffiliateCode,
  loadAffiliateCode,
  loadOAuthAffiliateCode,
  resolveAffiliateCode,
  storeAffiliateCode,
  storeOAuthAffiliateCode
} from '@/utils/oauthAffiliate'

describe('oauthAffiliate', () => {
  beforeEach(() => {
    localStorage.clear()
    sessionStorage.clear()
    vi.useRealTimers()
  })

  it('persists affiliate code across pages', () => {
    expect(resolveAffiliateCode(' 5579J7CFG9PF ')).toBe('5579J7CFG9PF')
    expect(loadAffiliateCode()).toBe('5579J7CFG9PF')
    expect(resolveAffiliateCode()).toBe('5579J7CFG9PF')
  })

  it('expires stale affiliate code', () => {
    const now = Date.UTC(2026, 0, 1)
    storeAffiliateCode('AFF123', now)

    expect(loadAffiliateCode(now + 30 * 24 * 60 * 60 * 1000 - 1)).toBe('AFF123')
    expect(loadAffiliateCode(now + 30 * 24 * 60 * 60 * 1000 + 1)).toBe('')
    expect(localStorage.getItem('affiliate_aff_code')).toBeNull()
  })

  it('keeps oauth transient code separate from persistent affiliate code', () => {
    storeAffiliateCode('PERSISTED')
    storeOAuthAffiliateCode('OAUTH')

    expect(loadAffiliateCode()).toBe('PERSISTED')
    expect(loadOAuthAffiliateCode()).toBe('OAUTH')

    clearOAuthAffiliateCode()
    expect(loadOAuthAffiliateCode()).toBe('')
    expect(loadAffiliateCode()).toBe('PERSISTED')

    clearAffiliateCode()
    expect(loadAffiliateCode()).toBe('')
  })
})
