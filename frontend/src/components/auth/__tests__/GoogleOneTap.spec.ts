import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  showGoogleOneTap: vi.fn(),
  cancelGoogleOneTap: vi.fn(),
  originSupported: true,
  credentialCallback: undefined as undefined | ((response: { credential: string }) => Promise<void>),
  exchangeGoogleOneTap: vi.fn(),
  persistOAuthTokenContext: vi.fn(),
  resolveAffiliateCode: vi.fn(),
  storeOAuthAffiliateCode: vi.fn(),
  clearAllAffiliateCodes: vi.fn(),
  setToken: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn(),
  push: vi.fn(),
  routeQuery: {} as Record<string, unknown>,
  isAuthenticated: false
}))

vi.mock('@/utils/googleIdentity', () => ({
  showGoogleOneTap: (clientID: string, callback: (response: { credential: string }) => Promise<void>) => {
    mocks.credentialCallback = callback
    return mocks.showGoogleOneTap(clientID, callback)
  },
  cancelGoogleOneTap: (...args: unknown[]) => mocks.cancelGoogleOneTap(...args),
  isGoogleOneTapOriginSupported: () => mocks.originSupported
}))

vi.mock('@/api/auth', () => ({
  exchangeGoogleOneTap: (...args: unknown[]) => mocks.exchangeGoogleOneTap(...args),
  persistOAuthTokenContext: (...args: unknown[]) => mocks.persistOAuthTokenContext(...args)
}))

vi.mock('@/utils/oauthAffiliate', () => ({
  resolveAffiliateCode: (...args: unknown[]) => mocks.resolveAffiliateCode(...args),
  storeOAuthAffiliateCode: (...args: unknown[]) => mocks.storeOAuthAffiliateCode(...args),
  clearAllAffiliateCodes: (...args: unknown[]) => mocks.clearAllAffiliateCodes(...args)
}))

vi.mock('@/utils/apiError', () => ({
  extractI18nErrorMessage: () => 'auth.loginFailed'
}))

vi.mock('@/stores', () => ({
  useAuthStore: () => ({
    get isAuthenticated() {
      return mocks.isAuthenticated
    },
    setToken: (...args: unknown[]) => mocks.setToken(...args)
  }),
  useAppStore: () => ({
    showSuccess: (...args: unknown[]) => mocks.showSuccess(...args),
    showError: (...args: unknown[]) => mocks.showError(...args)
  })
}))

vi.mock('vue-router', () => ({
  useRoute: () => ({ query: mocks.routeQuery }),
  useRouter: () => ({ push: (...args: unknown[]) => mocks.push(...args) })
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

import GoogleOneTap from '@/components/auth/GoogleOneTap.vue'

function mountOneTap(enabled = true) {
  return mount(GoogleOneTap, {
    props: {
      enabled,
      clientId: 'google-client'
    }
  })
}

describe('GoogleOneTap', () => {
  beforeEach(() => {
    for (const mock of [
      mocks.showGoogleOneTap,
      mocks.cancelGoogleOneTap,
      mocks.exchangeGoogleOneTap,
      mocks.persistOAuthTokenContext,
      mocks.resolveAffiliateCode,
      mocks.storeOAuthAffiliateCode,
      mocks.clearAllAffiliateCodes,
      mocks.setToken,
      mocks.showSuccess,
      mocks.showError,
      mocks.push
    ]) {
      mock.mockReset()
    }
    mocks.originSupported = true
    mocks.credentialCallback = undefined
    mocks.routeQuery = { redirect: '/usage', aff: 'AFF123', promo_code: 'PROMO123' }
    mocks.isAuthenticated = false
    mocks.showGoogleOneTap.mockResolvedValue(undefined)
    mocks.resolveAffiliateCode.mockReturnValue('AFF123')
    mocks.setToken.mockResolvedValue(undefined)
    mocks.push.mockResolvedValue(undefined)
    window.sessionStorage.clear()
  })

  it('persists an authenticated response and follows the requested local redirect', async () => {
    const response = {
      status: 'authenticated',
      access_token: 'access-token',
      refresh_token: 'refresh-token',
      expires_in: 3600,
      token_type: 'Bearer'
    }
    mocks.exchangeGoogleOneTap.mockResolvedValue(response)

    mountOneTap()
    await flushPromises()
    expect(mocks.showGoogleOneTap).toHaveBeenCalledWith('google-client', expect.any(Function))

    await mocks.credentialCallback?.({ credential: 'google-credential' })
    await flushPromises()

    expect(mocks.exchangeGoogleOneTap).toHaveBeenCalledWith({
      credential: 'google-credential',
      redirect: '/usage',
      aff_code: 'AFF123',
      promo_code: 'PROMO123'
    })
    expect(mocks.persistOAuthTokenContext).toHaveBeenCalledWith(response)
    expect(mocks.setToken).toHaveBeenCalledWith('access-token')
    expect(mocks.clearAllAffiliateCodes).toHaveBeenCalledOnce()
    expect(mocks.push).toHaveBeenCalledWith('/usage')
  })

  it('continues new users through the existing OAuth registration page', async () => {
    mocks.exchangeGoogleOneTap.mockResolvedValue({
      status: 'registration_required',
      redirect: '/dashboard'
    })

    mountOneTap()
    await flushPromises()
    await mocks.credentialCallback?.({ credential: 'google-credential' })
    await flushPromises()

    expect(window.sessionStorage.getItem('email_oauth_pending_provider')).toBe('google')
    expect(mocks.persistOAuthTokenContext).not.toHaveBeenCalled()
    expect(mocks.push).toHaveBeenCalledWith('/auth/oauth/callback')
  })

  it('keeps ordinary login available when the SDK is unavailable', async () => {
    mocks.showGoogleOneTap.mockRejectedValue(new Error('blocked'))
    mountOneTap()
    await flushPromises()

    expect(mocks.showError).not.toHaveBeenCalled()
  })

  it('reports credential exchange errors without removing ordinary login', async () => {
    mocks.exchangeGoogleOneTap.mockRejectedValue(new Error('invalid credential'))
    mountOneTap()
    await flushPromises()
    await mocks.credentialCallback?.({ credential: 'google-credential' })
    await flushPromises()

    expect(mocks.showError).toHaveBeenCalledWith('auth.loginFailed')
    expect(mocks.push).not.toHaveBeenCalled()
  })

  it('does not prompt for authenticated, disabled, or insecure sessions', async () => {
    const disabled = mountOneTap(false)
    await flushPromises()
    expect(mocks.showGoogleOneTap).not.toHaveBeenCalled()
    disabled.unmount()

    mocks.isAuthenticated = true
    mountOneTap()
    await flushPromises()
    expect(mocks.showGoogleOneTap).not.toHaveBeenCalled()

    mocks.isAuthenticated = false
    mocks.originSupported = false
    mountOneTap()
    await flushPromises()
    expect(mocks.showGoogleOneTap).not.toHaveBeenCalled()
  })

  it('cancels the browser prompt when disabled or unmounted', async () => {
    const wrapper = mountOneTap()
    await flushPromises()
    await wrapper.setProps({ enabled: false })
    expect(mocks.cancelGoogleOneTap).toHaveBeenCalled()

    mocks.cancelGoogleOneTap.mockClear()
    wrapper.unmount()
    expect(mocks.cancelGoogleOneTap).toHaveBeenCalled()
  })
})
