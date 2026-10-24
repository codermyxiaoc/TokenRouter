import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

type NavigationGuard = (
  to: Record<string, any>,
  from: Record<string, any>,
  next: ReturnType<typeof vi.fn>
) => Promise<void>

const routerHarness = vi.hoisted(() => ({
  guard: null as NavigationGuard | null,
  routes: [] as Array<{ path: string, meta?: Record<string, unknown> }>,
}))

const authStore = vi.hoisted(() => ({
  checkAuth: vi.fn(),
  isAuthenticated: true,
  isAdmin: false,
  isSimpleMode: false,
  hasPendingAuthSession: false,
}))

const appStore = vi.hoisted(() => ({
  siteName: 'Sub2API',
  backendModeEnabled: false,
  publicSettingsLoaded: false,
  cachedPublicSettings: null as null | {
    payment_enabled?: boolean
    risk_control_enabled?: boolean
    team_enabled?: boolean
    ticket_enabled?: boolean
    intelligence_enabled?: boolean
    daily_activity_enabled?: boolean
    usage_ranking_enabled?: boolean
    custom_menu_items?: []
  },
  fetchPublicSettings: vi.fn(),
}))

const adminSettingsStore = vi.hoisted(() => ({
  customMenuItems: [],
  chanshuiEnabled: false,
  refreshChanshuiEnabled: vi.fn(),
}))

vi.mock('vue-router', () => ({
  createWebHistory: vi.fn(() => ({})),
  createRouter: vi.fn((options) => {
    routerHarness.routes = options.routes
    return {
    beforeEach: vi.fn((guard: NavigationGuard) => {
      routerHarness.guard = guard
    }),
    afterEach: vi.fn(),
    onError: vi.fn(),
    }
  }),
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => authStore,
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => appStore,
}))

vi.mock('@/stores/adminSettings', () => ({
  useAdminSettingsStore: () => adminSettingsStore,
}))

vi.mock('@/stores/adminCompliance', () => ({
  useAdminComplianceStore: () => ({
    initialized: true,
    fetchStatus: vi.fn(),
    requireAcknowledgement: vi.fn(),
  }),
}))

vi.mock('@/composables/useNavigationLoading', () => ({
  useNavigationLoadingState: () => ({
    startNavigation: vi.fn(),
    endNavigation: vi.fn(),
    isLoading: { value: false },
  }),
}))

vi.mock('@/composables/useRoutePrefetch', () => ({
  useRoutePrefetch: () => ({
    triggerPrefetch: vi.fn(),
    cancelPendingPrefetch: vi.fn(),
    resetPrefetchState: vi.fn(),
  }),
}))

function createDeferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise
  })
  return { promise, resolve }
}

function runGuard(meta: Record<string, unknown>, path: string) {
  if (!routerHarness.guard) {
    throw new Error('router guard was not registered')
  }

  const next = vi.fn()
  const navigation = routerHarness.guard(
    {
      path,
      fullPath: path,
      name: 'FeatureRoute',
      params: {},
      meta: { requiresAuth: true, ...meta },
    },
    {},
    next
  )
  return { navigation, next }
}

describe('feature route guard', () => {
  beforeAll(async () => {
    await import('@/router')
  })

  beforeEach(() => {
    authStore.isAuthenticated = true
    authStore.isAdmin = false
    authStore.isSimpleMode = false
    appStore.publicSettingsLoaded = false
    appStore.cachedPublicSettings = null
    appStore.fetchPublicSettings.mockReset()
    adminSettingsStore.chanshuiEnabled = false
    adminSettingsStore.refreshChanshuiEnabled.mockReset().mockResolvedValue(undefined)
  })

  it('daily activities loads settings but keeps personal history accessible when disabled', async () => {
    const route = routerHarness.routes.find(route => route.path === '/daily-activities')!
    expect(route.meta?.requiresAuth).toBe(true)
    expect(route.meta?.loadsDailyActivitySettings).toBe(true)
    appStore.fetchPublicSettings.mockImplementation(async () => {
      appStore.publicSettingsLoaded = true
      appStore.cachedPublicSettings = { daily_activity_enabled: false }
    })
    const { navigation, next } = runGuard(route.meta!, '/daily-activities')
    await navigation
    expect(appStore.fetchPublicSettings).toHaveBeenCalledTimes(1)
    expect(next).toHaveBeenCalledWith()
  })

  it('每日活动未登录不能读取个人页面，登录后保留返回地址', async () => {
    authStore.isAuthenticated = false
    const route = routerHarness.routes.find(route => route.path === '/daily-activities')!
    const { navigation, next } = runGuard(route.meta!, '/daily-activities')
    await navigation
    expect(next).toHaveBeenCalledWith({ path: '/login', query: { redirect: '/daily-activities' } })
    expect(appStore.fetchPublicSettings).not.toHaveBeenCalled()
  })

  it('普通用户不能通过系统设置地址进入活动管理配置', async () => {
    const route = routerHarness.routes.find(route => route.path === '/admin/settings')!
    expect(route.meta?.requiresAdmin).toBe(true)
    const { navigation, next } = runGuard(route.meta!, '/admin/settings')
    await navigation
    expect(next).toHaveBeenCalledWith('/dashboard')
    expect(appStore.fetchPublicSettings).not.toHaveBeenCalled()
  })

  it('waits for the first public-settings request before deciding payment access', async () => {
    const deferred = createDeferred<{ payment_enabled: boolean }>()
    appStore.fetchPublicSettings.mockImplementation(async () => {
      const settings = await deferred.promise
      appStore.cachedPublicSettings = settings
      appStore.publicSettingsLoaded = true
      return settings
    })

    const { navigation, next } = runGuard({ requiresPayment: true }, '/purchase')

    await vi.waitFor(() => expect(appStore.fetchPublicSettings).toHaveBeenCalledTimes(1))
    expect(next).not.toHaveBeenCalled()

    deferred.resolve({ payment_enabled: true })
    await navigation
    expect(next).toHaveBeenCalledOnce()
    expect(next).toHaveBeenCalledWith()
  })

  it.each([
    ['payment', { requiresPayment: true }, '/purchase'],
    ['risk control', { requiresRiskControl: true }, '/admin/risk-control'],
    ['team', { requiresTeam: true }, '/team'],
    ['usage ranking', { requiresUsageRanking: true }, '/usage-ranking'],
    ['creative', { requiresCreative: true }, '/creative'],
    ['tickets', { requiresTickets: true }, '/tickets'],
  ])('does not treat a failed %s settings load as explicitly disabled', async (_name, meta, path) => {
    authStore.isAdmin = path.startsWith('/admin/')
    appStore.fetchPublicSettings.mockResolvedValue(null)

    const { navigation, next } = runGuard(meta, path)
    await navigation

    expect(appStore.publicSettingsLoaded).toBe(false)
    expect(next).toHaveBeenCalledOnce()
    expect(next).toHaveBeenCalledWith()
  })

  it.each([
    ['payment', { requiresPayment: true }, { payment_enabled: false }, '/dashboard', false],
    [
      'risk control',
      { requiresRiskControl: true },
      { risk_control_enabled: false },
      '/admin/settings',
      true,
    ],
    ['team', { requiresTeam: true }, { team_enabled: false }, '/dashboard', false],
    ['admin team', { requiresTeam: true }, { team_enabled: false }, '/admin/settings', true],
    ['usage ranking', { requiresUsageRanking: true }, { usage_ranking_enabled: false }, '/dashboard', false],
    ['creative', { requiresCreative: true }, { creative_enabled: false }, '/dashboard', false],
    ['admin creative', { requiresCreative: true }, { creative_enabled: false }, '/admin/settings', true],
    ['tickets', { requiresTickets: true }, { ticket_enabled: false }, '/dashboard', false],
    ['admin tickets', { requiresTickets: true }, { ticket_enabled: false }, '/admin/dashboard', true],
  ])('redirects when loaded settings explicitly disable %s', async (_name, meta, settings, target, isAdmin) => {
    authStore.isAdmin = isAdmin
    appStore.cachedPublicSettings = settings
    appStore.publicSettingsLoaded = true

    const { navigation, next } = runGuard(meta, '/feature')
    await navigation

    expect(appStore.fetchPublicSettings).not.toHaveBeenCalled()
    expect(next).toHaveBeenCalledOnce()
    expect(next).toHaveBeenCalledWith(target)
  })

  it.each(['/tickets', '/tickets/:id', '/tickets/:id/attachments/:attachmentId/preview', '/admin/tickets', '/admin/tickets/:id', '/admin/tickets/:id/attachments/:attachmentId/preview'])('gates registered ticket route %s while allowing older public settings', async (path) => {
    const route = routerHarness.routes.find(item => item.path === path)!
    expect(route.meta?.requiresTickets).toBe(true)
    authStore.isAdmin = path.startsWith('/admin/')
    appStore.publicSettingsLoaded = true
    appStore.cachedPublicSettings = {}
    const { navigation, next } = runGuard(route.meta!, path)
    await navigation
    expect(next).toHaveBeenCalledWith()
  })

  it('keeps system settings available after tickets are disabled', async () => {
    const route = routerHarness.routes.find(item => item.path === '/admin/settings')!
    expect(route.meta?.requiresTickets).not.toBe(true)
    authStore.isAdmin = true
    appStore.publicSettingsLoaded = true
    appStore.cachedPublicSettings = { ticket_enabled: false }
    const { navigation, next } = runGuard(route.meta!, route.path)
    await navigation
    expect(next).toHaveBeenCalledWith()
  })

  it.each([undefined, false])('keeps intelligence results closed when the flag is %s', async (enabled) => {
    appStore.publicSettingsLoaded = true
    appStore.cachedPublicSettings = { intelligence_enabled: enabled }
    const route = routerHarness.routes.find(item => item.path === '/intelligence')!
    expect(route.meta?.requiresIntelligence).toBe(true)
    const { navigation, next } = runGuard(route.meta!, route.path)
    await navigation
    expect(next).toHaveBeenCalledWith('/dashboard')
  })

  it('loads intelligence settings before allowing the user route', async () => {
    appStore.fetchPublicSettings.mockImplementation(async () => {
      appStore.cachedPublicSettings = { intelligence_enabled: true }
      appStore.publicSettingsLoaded = true
      return appStore.cachedPublicSettings
    })
    const { navigation, next } = runGuard({ requiresIntelligence: true }, '/intelligence')
    await navigation
    expect(appStore.fetchPublicSettings).toHaveBeenCalledOnce()
    expect(next).toHaveBeenCalledWith()
  })

  it('restricts model integrity checks to administrators without a public feature flag', async () => {
    const route = routerHarness.routes.find(item => item.path === '/admin/chanshui-tests')!
    expect(route.meta?.requiresAdmin).toBe(true)
    expect(route.meta?.requiresAuth).toBe(true)
    expect(route.meta?.requiresChanshui).toBe(true)
    expect(routerHarness.routes.some(item => item.path === '/chanshui-tests')).toBe(false)
    const { navigation, next } = runGuard(route.meta!, route.path)
    await navigation
    expect(next).toHaveBeenCalledWith('/dashboard')
    expect(adminSettingsStore.refreshChanshuiEnabled).not.toHaveBeenCalled()
    expect(appStore.fetchPublicSettings).not.toHaveBeenCalled()
  })

  it('未登录访问掺水页先登录，不读取管理员设置', async () => {
    authStore.isAuthenticated = false
    const route = routerHarness.routes.find(item => item.path === '/admin/chanshui-tests')!
    const { navigation, next } = runGuard(route.meta!, route.path)
    await navigation
    expect(next).toHaveBeenCalledWith({ path: '/login', query: { redirect: route.path } })
    expect(adminSettingsStore.refreshChanshuiEnabled).not.toHaveBeenCalled()
    expect(appStore.fetchPublicSettings).not.toHaveBeenCalled()
  })

  it('掺水开关关闭时直接地址退回功能设置', async () => {
    authStore.isAdmin = true
    const route = routerHarness.routes.find(item => item.path === '/admin/chanshui-tests')!
    const { navigation, next } = runGuard(route.meta!, route.path)
    await navigation
    expect(adminSettingsStore.refreshChanshuiEnabled).toHaveBeenCalledOnce()
    expect(next).toHaveBeenCalledWith('/admin/settings?tab=features')
    expect(appStore.fetchPublicSettings).not.toHaveBeenCalled()
  })

  it('等待私有开关刷新后允许已开启的掺水页，不依赖公共设置', async () => {
    authStore.isAdmin = true
    const deferred = createDeferred<void>()
    adminSettingsStore.refreshChanshuiEnabled.mockImplementation(async () => {
      await deferred.promise
      adminSettingsStore.chanshuiEnabled = true
    })
    const route = routerHarness.routes.find(item => item.path === '/admin/chanshui-tests')!
    const allowed = runGuard(route.meta!, route.path)
    expect(allowed.next).not.toHaveBeenCalled()
    deferred.resolve()
    await allowed.navigation
    expect(allowed.next).toHaveBeenCalledWith()
    expect(appStore.fetchPublicSettings).not.toHaveBeenCalled()
  })

  it('已打开过的掺水页再次进入时重新核对开关', async () => {
    authStore.isAdmin = true
    adminSettingsStore.chanshuiEnabled = true
    adminSettingsStore.refreshChanshuiEnabled.mockImplementation(async () => {
      adminSettingsStore.chanshuiEnabled = false
    })
    const route = routerHarness.routes.find(item => item.path === '/admin/chanshui-tests')!
    const { navigation, next } = runGuard(route.meta!, route.path)
    await navigation
    expect(next).toHaveBeenCalledWith('/admin/settings?tab=features')
  })

  it('掺水关闭时系统设置仍可进入以重新开启', async () => {
    authStore.isAdmin = true
    const route = routerHarness.routes.find(item => item.path === '/admin/settings')!
    const { navigation, next } = runGuard(route.meta!, route.path)
    await navigation
    expect(next).toHaveBeenCalledWith()
    expect(adminSettingsStore.refreshChanshuiEnabled).not.toHaveBeenCalled()
  })

  it('keeps intelligence administration reachable while the feature is off', async () => {
    authStore.isAdmin = true
    appStore.publicSettingsLoaded = true
    appStore.cachedPublicSettings = { intelligence_enabled: false }
    const route = routerHarness.routes.find(item => item.path === '/admin/intelligence-tests')!
    expect(route.meta?.requiresAdmin).toBe(true)
    expect(route.meta?.requiresIntelligence).not.toBe(true)
    const { navigation, next } = runGuard(route.meta!, route.path)
    await navigation
    expect(next).toHaveBeenCalledWith()
  })
})
