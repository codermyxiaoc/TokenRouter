import { defineComponent, h } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import LoginView from '@/views/auth/LoginView.vue'

const loginMock = vi.fn()
const loginWithPasskeyMock = vi.fn()
const getPublicSettingsMock = vi.fn()
const startOAuthLoginMock = vi.fn()
const verifyActionMock = vi.fn()
const captchaResetMock = vi.fn()
const oneTapCancelMock = vi.fn()
const locationState = {
  href: 'http://localhost/login',
  protocol: 'http:',
  hostname: 'localhost'
}

vi.mock('vue-router', () => ({
  useRouter: () => ({
    currentRoute: { value: { query: {} } },
    push: vi.fn()
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

vi.mock('@/stores', () => ({
  useAuthStore: () => ({
    login: (...args: unknown[]) => loginMock(...args),
    loginWithPasskey: (...args: unknown[]) => loginWithPasskeyMock(...args)
  }),
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showWarning: vi.fn()
  })
}))

vi.mock('@/api/auth', async () => {
  const actual = await vi.importActual<typeof import('@/api/auth')>('@/api/auth')
  return {
    ...actual,
    getPublicSettings: (...args: unknown[]) => getPublicSettingsMock(...args),
    startOAuthLogin: (...args: unknown[]) => startOAuthLoginMock(...args),
    isTotp2FARequired: () => false,
    isWeChatWebOAuthEnabled: () => false
  }
})

const CaptchaChallengeStub = defineComponent({
  setup(_, { expose }) {
    expose({
      verifyAction: verifyActionMock,
      reset: captchaResetMock
    })
    return () => h('div')
  }
})

const OAuthButtonStub = defineComponent({
  emits: ['start'],
  setup(_, { emit }) {
    return () => h('button', {
      type: 'button',
      'data-testid': 'oauth-start',
      onClick: () => emit('start', {
        provider: 'github',
        params: { redirect: '/dashboard' }
      })
    })
  }
})

const GoogleOneTapStub = defineComponent({
  name: 'GoogleOneTap',
  props: {
    enabled: Boolean,
    clientId: String
  },
  setup(props, { expose }) {
    expose({ cancelPrompt: oneTapCancelMock })
    return () => h('div', {
      'data-testid': 'google-one-tap',
      'data-enabled': String(props.enabled),
      'data-client-id': props.clientId
    })
  }
})

const LoginAgreementStub = defineComponent({
  name: 'LoginAgreementPrompt',
  emits: ['accept', 'reject', 'open'],
  setup(_, { emit }) {
    return () => h('button', {
      type: 'button',
      'data-testid': 'accept-login-agreement',
      onClick: () => emit('accept')
    })
  }
})

function mountLogin() {
  return mount(LoginView, {
    global: {
      stubs: {
        AuthLayout: { template: '<div><slot /><slot name="footer" /></div>' },
        RouterLink: true,
        TurnstileWidget: CaptchaChallengeStub,
        Icon: true,
        LoginAgreementPrompt: LoginAgreementStub,
        TotpLoginModal: true,
        GoogleOneTap: GoogleOneTapStub,
        EmailOAuthButtons: OAuthButtonStub,
        LinuxDoOAuthSection: true,
        DingTalkOAuthSection: true,
        OidcOAuthSection: true,
        WechatOAuthSection: true
      }
    }
  })
}

function enableAliyunCaptcha(): void {
  getPublicSettingsMock.mockResolvedValue({
    turnstile_enabled: false,
    turnstile_site_key: '',
    tencent_captcha_enabled: false,
    tencent_captcha_app_id: '',
    aliyun_captcha_enabled: true,
    aliyun_captcha_scene_id: 'scene-1',
    aliyun_captcha_prefix: 'prefix-1',
    aliyun_captcha_region: 'cn',
    backend_mode_enabled: false,
    password_reset_enabled: false,
    passkey_enabled: true,
    github_oauth_enabled: true,
    google_oauth_enabled: false
  })
  verifyActionMock.mockResolvedValue({ token: 'aliyun-param-1', randstr: '' })
}

describe('Action captcha gate', () => {
  beforeEach(() => {
    loginMock.mockReset()
    loginWithPasskeyMock.mockReset()
    getPublicSettingsMock.mockReset()
    startOAuthLoginMock.mockReset()
    verifyActionMock.mockReset()
    captchaResetMock.mockReset()
    oneTapCancelMock.mockReset()
    getPublicSettingsMock.mockResolvedValue({
      turnstile_enabled: false,
      turnstile_site_key: '',
      tencent_captcha_enabled: true,
      tencent_captcha_app_id: 'tencent-app-id',
      backend_mode_enabled: false,
      password_reset_enabled: false,
      passkey_enabled: true,
      github_oauth_enabled: true,
      google_oauth_enabled: false
    })
    loginMock.mockResolvedValue({})
    loginWithPasskeyMock.mockResolvedValue({})
    startOAuthLoginMock.mockResolvedValue({ authorize_url: 'https://github.example/authorize' })
    verifyActionMock.mockResolvedValue({ token: 'ticket-1', randstr: '@rand-1' })
    Object.defineProperty(window, 'PublicKeyCredential', {
      configurable: true,
      value: class PublicKeyCredential {}
    })
    locationState.href = 'http://localhost/login'
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: locationState
    })
  })

  it('clicking login opens Tencent captcha before calling login', async () => {
    const wrapper = mountLogin()
    await flushPromises()
    await wrapper.get('#email').setValue('user@example.com')
    await wrapper.get('#password').setValue('secret-123')

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(verifyActionMock).toHaveBeenCalledOnce()
    expect(loginMock).toHaveBeenCalledWith(expect.objectContaining({
      tencent_captcha_ticket: 'ticket-1',
      tencent_captcha_randstr: '@rand-1'
    }))
  })

  it('does not call login when Tencent captcha is closed', async () => {
    verifyActionMock.mockResolvedValue(null)
    const wrapper = mountLogin()
    await flushPromises()
    await wrapper.get('#email').setValue('user@example.com')
    await wrapper.get('#password').setValue('secret-123')

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(verifyActionMock).toHaveBeenCalledOnce()
    expect(loginMock).not.toHaveBeenCalled()
  })

  it('does not open Tencent captcha when login form validation fails', async () => {
    const wrapper = mountLogin()
    await flushPromises()

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(verifyActionMock).not.toHaveBeenCalled()
    expect(loginMock).not.toHaveBeenCalled()
  })

  it('starts OAuth through the Tencent gate before navigating', async () => {
    const wrapper = mountLogin()
    await flushPromises()

    await wrapper.get('[data-testid="oauth-start"]').trigger('click')
    await flushPromises()

    expect(verifyActionMock).toHaveBeenCalledOnce()
    expect(startOAuthLoginMock).toHaveBeenCalledWith(
      { provider: 'github', params: { redirect: '/dashboard' } },
      {
        tencent_captcha_ticket: 'ticket-1',
        tencent_captcha_randstr: '@rand-1'
      }
    )
    expect(locationState.href).toBe('https://github.example/authorize')
    expect(captchaResetMock).toHaveBeenCalledOnce()
  })

  it('does not start OAuth when Tencent captcha is closed', async () => {
    verifyActionMock.mockResolvedValue(null)
    const wrapper = mountLogin()
    await flushPromises()

    await wrapper.get('[data-testid="oauth-start"]').trigger('click')
    await flushPromises()

    expect(startOAuthLoginMock).not.toHaveBeenCalled()
    expect(locationState.href).toBe('http://localhost/login')
  })

  it('passes a fresh Tencent proof to Passkey login', async () => {
    const wrapper = mountLogin()
    await flushPromises()

    await wrapper.get('button.btn-secondary.w-full').trigger('click')
    await flushPromises()

    expect(verifyActionMock).toHaveBeenCalledOnce()
    expect(loginWithPasskeyMock).toHaveBeenCalledWith({
      tencent_captcha_ticket: 'ticket-1',
      tencent_captcha_randstr: '@rand-1'
    })
    expect(captchaResetMock).toHaveBeenCalledOnce()
  })

  it('does not invoke Passkey when Tencent captcha is closed', async () => {
    verifyActionMock.mockResolvedValue(null)
    const wrapper = mountLogin()
    await flushPromises()

    await wrapper.get('button.btn-secondary.w-full').trigger('click')
    await flushPromises()

    expect(loginWithPasskeyMock).not.toHaveBeenCalled()
  })

  it('passes the Aliyun verification parameter to password login', async () => {
    enableAliyunCaptcha()
    const wrapper = mountLogin()
    await flushPromises()
    await wrapper.get('#email').setValue('user@example.com')
    await wrapper.get('#password').setValue('secret-123')

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(verifyActionMock).toHaveBeenCalledOnce()
    expect(loginMock).toHaveBeenCalledWith(expect.objectContaining({
      turnstile_token: 'aliyun-param-1',
      tencent_captcha_ticket: undefined,
      tencent_captcha_randstr: undefined
    }))
  })

  it('passes the Aliyun verification parameter to OAuth start', async () => {
    enableAliyunCaptcha()
    const wrapper = mountLogin()
    await flushPromises()

    await wrapper.get('[data-testid="oauth-start"]').trigger('click')
    await flushPromises()

    expect(verifyActionMock).toHaveBeenCalledOnce()
    expect(startOAuthLoginMock).toHaveBeenCalledWith(
      { provider: 'github', params: { redirect: '/dashboard' } },
      { turnstile_token: 'aliyun-param-1' }
    )
  })

  it('passes the Aliyun verification parameter to Passkey login', async () => {
    enableAliyunCaptcha()
    const wrapper = mountLogin()
    await flushPromises()

    await wrapper.get('button.btn-secondary.w-full').trigger('click')
    await flushPromises()

    expect(verifyActionMock).toHaveBeenCalledOnce()
    expect(loginWithPasskeyMock).toHaveBeenCalledWith({
      turnstile_token: 'aliyun-param-1'
    })
  })

  it('enables One Tap only after the current login agreement is accepted', async () => {
    getPublicSettingsMock.mockResolvedValue({
      turnstile_enabled: false,
      turnstile_site_key: '',
      tencent_captcha_enabled: false,
      aliyun_captcha_enabled: false,
      backend_mode_enabled: false,
      password_reset_enabled: false,
      passkey_enabled: false,
      github_oauth_enabled: false,
      google_oauth_enabled: true,
      google_one_tap_enabled: true,
      google_oauth_client_id: 'google-client',
      login_agreement_enabled: true,
      login_agreement_mode: 'modal',
      login_agreement_revision: 'revision-1',
      login_agreement_documents: [{
        id: 'terms',
        title: 'Terms',
        content_md: 'Terms content'
      }]
    })

    const wrapper = mountLogin()
    await flushPromises()
    expect(wrapper.get('[data-testid="google-one-tap"]').attributes('data-enabled')).toBe('false')

    await wrapper.get('[data-testid="accept-login-agreement"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="google-one-tap"]').attributes('data-enabled')).toBe('true')
  })

  it('cancels One Tap when the user starts entering a password login', async () => {
    getPublicSettingsMock.mockResolvedValue({
      turnstile_enabled: false,
      tencent_captcha_enabled: false,
      aliyun_captcha_enabled: false,
      backend_mode_enabled: false,
      password_reset_enabled: false,
      passkey_enabled: false,
      github_oauth_enabled: false,
      google_oauth_enabled: true,
      google_one_tap_enabled: true,
      google_oauth_client_id: 'google-client'
    })
    const wrapper = mountLogin()
    await flushPromises()

    await wrapper.get('#email').setValue('user@example.com')
    expect(oneTapCancelMock).toHaveBeenCalled()
  })
})
