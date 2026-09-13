const GOOGLE_IDENTITY_SCRIPT_ID = 'google-identity-services'
const GOOGLE_IDENTITY_SCRIPT_SRC = 'https://accounts.google.com/gsi/client'

export interface GoogleCredentialResponse {
  credential: string
  select_by?: string
}

export interface GoogleOneTapEligibility {
  publicSettingsLoaded: boolean
  isAuthenticated: boolean
  oneTapEnabled: boolean
  clientID: string
  backendModeEnabled: boolean
  tencentCaptchaEnabled: boolean
  aliyunCaptchaEnabled: boolean
  loginAgreementEnabled: boolean
  loginAgreementAccepted: boolean
  originSupported: boolean
}

interface GoogleIdentityConfiguration {
  client_id: string
  callback: (response: GoogleCredentialResponse) => void
  auto_select: boolean
  context: 'signin'
  cancel_on_tap_outside: boolean
}

interface GoogleIdentityClient {
  initialize(config: GoogleIdentityConfiguration): void
  prompt(): void
  cancel(): void
}

declare global {
  interface Window {
    google?: {
      accounts?: {
        id?: GoogleIdentityClient
      }
    }
  }
}

let loadPromise: Promise<GoogleIdentityClient> | null = null
let initializedClientID = ''
let activeCallback: ((response: GoogleCredentialResponse) => void) | null = null

function currentGoogleIdentityClient(): GoogleIdentityClient | null {
  return window.google?.accounts?.id ?? null
}

// GIS 必须从 Google 官方地址加载；失败后允许后续页面重新尝试。
export function loadGoogleIdentityServices(): Promise<GoogleIdentityClient> {
  const existingClient = currentGoogleIdentityClient()
  if (existingClient) return Promise.resolve(existingClient)
  if (loadPromise) return loadPromise

  loadPromise = new Promise<GoogleIdentityClient>((resolve, reject) => {
    let script = document.getElementById(GOOGLE_IDENTITY_SCRIPT_ID) as HTMLScriptElement | null
    const handleLoad = () => {
      const client = currentGoogleIdentityClient()
      if (!client) {
        script?.remove()
        loadPromise = null
        reject(new Error('Google Identity Services did not initialize'))
        return
      }
      resolve(client)
    }
    const handleError = () => {
      script?.remove()
      loadPromise = null
      reject(new Error('Failed to load Google Identity Services'))
    }

    if (!script) {
      script = document.createElement('script')
      script.id = GOOGLE_IDENTITY_SCRIPT_ID
      script.src = GOOGLE_IDENTITY_SCRIPT_SRC
      script.async = true
      script.addEventListener('load', handleLoad, { once: true })
      script.addEventListener('error', handleError, { once: true })
      document.head.appendChild(script)
      return
    }

    script.addEventListener('load', handleLoad, { once: true })
    script.addEventListener('error', handleError, { once: true })
  })
  return loadPromise
}

// 同一个 SPA 只初始化一个 GIS client，页面切换时仅替换当前凭据回调。
export async function showGoogleOneTap(
  clientID: string,
  callback: (response: GoogleCredentialResponse) => void
): Promise<void> {
  const normalizedClientID = clientID.trim()
  if (!normalizedClientID) return

  activeCallback = callback
  const client = await loadGoogleIdentityServices()
  if (activeCallback !== callback) return

  if (initializedClientID !== normalizedClientID) {
    client.initialize({
      client_id: normalizedClientID,
      callback: (response) => activeCallback?.(response),
      auto_select: false,
      context: 'signin',
      cancel_on_tap_outside: true
    })
    initializedClientID = normalizedClientID
  }
  client.prompt()
}

export function cancelGoogleOneTap(
  callback?: (response: GoogleCredentialResponse) => void
): void {
  if (callback && activeCallback !== callback) return
  activeCallback = null
  currentGoogleIdentityClient()?.cancel()
}

export function isGoogleOneTapOriginSupported(locationValue: Location = window.location): boolean {
  if (locationValue.protocol === 'https:') return true
  if (locationValue.protocol !== 'http:') return false
  return ['localhost', '127.0.0.1', '::1', '[::1]'].includes(locationValue.hostname)
}

// One Tap 入口必须同时满足公开配置、登录状态、安全 Origin、验证码和协议门禁。
export function isGoogleOneTapEligible(options: GoogleOneTapEligibility): boolean {
  return options.publicSettingsLoaded &&
    !options.isAuthenticated &&
    options.oneTapEnabled &&
    Boolean(options.clientID.trim()) &&
    !options.backendModeEnabled &&
    !options.tencentCaptchaEnabled &&
    !options.aliyunCaptchaEnabled &&
    (!options.loginAgreementEnabled || options.loginAgreementAccepted) &&
    options.originSupported
}
