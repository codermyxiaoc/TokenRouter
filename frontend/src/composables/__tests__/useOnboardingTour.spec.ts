import { defineComponent, nextTick } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { useOnboardingTour } from '../useOnboardingTour'

const { driverFactory, routerPush, currentRoute, onboardingStore } = vi.hoisted(() => ({
  driverFactory: vi.fn(),
  routerPush: vi.fn(),
  currentRoute: { value: { fullPath: '/team' } },
  onboardingStore: {
    getDriverInstance: vi.fn(() => null),
    setDriverInstance: vi.fn(),
    isDriverActive: vi.fn(() => false),
    setControlMethods: vi.fn(),
    clearControlMethods: vi.fn(),
  },
}))

vi.mock('driver.js', () => ({ driver: driverFactory }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ user: { id: 7, role: 'user' }, isSimpleMode: false }),
}))
vi.mock('@/stores/onboarding', () => ({ useOnboardingStore: () => onboardingStore }))
vi.mock('vue-router', () => ({
  useRouter: () => ({
    currentRoute,
    resolve: (route: { path: string, query?: Record<string, string> }) => ({
      fullPath: route.query?.scope ? `${route.path}?scope=${route.query.scope}` : route.path,
    }),
    push: routerPush,
  }),
}))

describe('useOnboardingTour team flow', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    currentRoute.value = { fullPath: '/team' }
    routerPush.mockImplementation(async (route: { path: string, query?: Record<string, string> }) => {
      currentRoute.value = {
        fullPath: route.query?.scope ? `${route.path}?scope=${route.query.scope}` : route.path,
      }
      await nextTick()
    })
  })

  it('在团队、密钥和使用记录步骤之间按目标路由切换', async () => {
    let activeIndex = 0
    let active = true
    let config: any
    const driverInstance = {
      drive: vi.fn((index = 0) => { activeIndex = index }),
      moveTo: vi.fn((index: number) => { activeIndex = index }),
      moveNext: vi.fn(() => { activeIndex += 1 }),
      movePrevious: vi.fn(() => { activeIndex -= 1 }),
      getActiveIndex: vi.fn(() => activeIndex),
      getActiveElement: vi.fn(() => null),
      refresh: vi.fn(),
      isActive: vi.fn(() => active),
      destroy: vi.fn(() => {
        active = false
        config?.onDestroyed?.()
      }),
    }
    driverFactory.mockImplementation((nextConfig) => {
      config = nextConfig
      return driverInstance
    })

    // 测试中为所有引导锚点提供可见元素，聚焦验证路由控制逻辑。
    const target = document.createElement('div')
    vi.spyOn(target, 'getBoundingClientRect').mockReturnValue({ height: 40 } as DOMRect)
    const querySelector = vi.spyOn(document, 'querySelector').mockReturnValue(target)

    const Harness = defineComponent({
      setup() {
        return useOnboardingTour({ autoStart: false })
      },
      template: '<div />',
    })
    const wrapper = mount(Harness)
    await flushPromises()

    const startTeamTour = (wrapper.vm as any).startTeamTour as (options: { isOwner: boolean, hasTeam: boolean }) => void
    startTeamTour({ isOwner: true, hasTeam: true })
    await flushPromises()

    // 页码保留给用户确认导览进度，键盘提示由自定义 Footer 清理逻辑移除。
    expect(config.showProgress).toBe(true)

    const reveal = vi.fn()
    target.addEventListener('onboarding-reveal', reveal)
    await config.onHighlightStarted(target, { element: '[data-tour="group-form-multiplier"]' })
    expect(reveal).toHaveBeenCalledOnce()
    expect(driverInstance.refresh).toHaveBeenCalledOnce()

    for (let index = 0; index < 5; index += 1) {
      await config.onNextClick(null, config.steps[index], {
        config,
        state: { activeIndex: index },
      })
    }

    expect(routerPush).toHaveBeenCalledWith({ path: '/keys', query: { scope: 'team' } })
    expect(driverInstance.moveTo).toHaveBeenLastCalledWith(5)

    await config.onPrevClick(null, config.steps[5], { state: { activeIndex: 5 } })
    expect(routerPush).toHaveBeenLastCalledWith({ path: '/team' })
    expect(driverInstance.moveTo).toHaveBeenLastCalledWith(4)

    config.onCloseClick()
    wrapper.unmount()
    querySelector.mockRestore()
  })
})
