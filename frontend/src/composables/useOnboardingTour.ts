import { onMounted, onUnmounted, nextTick } from 'vue'
import { driver, type Driver, type DriveStep } from 'driver.js'
import 'driver.js/dist/driver.css'
import { useAuthStore as useUserStore } from '@/stores/auth'
import { useOnboardingStore, type TeamGuideOptions } from '@/stores/onboarding'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { getAdminSteps, getTeamSteps, getUserSteps, type RoutedDriveStep } from '@/components/Guide/steps'

export interface OnboardingOptions {
  storageKey?: string
  autoStart?: boolean
}

export function useOnboardingTour(options: OnboardingOptions) {
  const { t } = useI18n()
  const userStore = useUserStore()
  const onboardingStore = useOnboardingStore()
  const router = useRouter()
  const storageVersion = 'v4_interactive' // Bump version for new tour type
  const teamStorageVersion = 'v1_routed'

  // Timing constants for better maintainability
  const TIMING = {
    INTERACTIVE_WAIT_MS: 800,        // Default wait time for interactive steps
    ELEMENT_TIMEOUT_MS: 8000,        // Timeout for element detection
    AUTO_START_DELAY_MS: 1000        // Delay before auto-starting tour
  } as const

  // Helper: Check if a step is interactive (only close button shown)
  const isInteractiveStep = (step: DriveStep): boolean => {
    return step.popover?.showButtons?.length === 1 &&
           step.popover.showButtons[0] === 'close'
  }

  // Helper: Clean up click listener
  const cleanupClickListener = () => {
    if (!currentClickListener) return
    const { element: el, handler, keyHandler, originalTabIndex, eventTypes } = currentClickListener
    if (eventTypes) {
      eventTypes.forEach(type => el.removeEventListener(type, handler))
    }
    if (keyHandler) el.removeEventListener('keydown', keyHandler)
    if (originalTabIndex !== undefined) {
      if (originalTabIndex === null) el.removeAttribute('tabindex')
      else el.setAttribute('tabindex', originalTabIndex)
    }
    currentClickListener = null
  }

  // 使用 store 管理的全局 driver 实例
  let driverInstance: Driver | null = onboardingStore.getDriverInstance()
  let currentClickListener: {
    element: HTMLElement
    handler: () => void
    keyHandler?: (e: KeyboardEvent) => void
    originalTabIndex?: string | null
    eventTypes?: string[] // Track which event types were added
  } | null = null
  let autoStartTimer: ReturnType<typeof setTimeout> | null = null
  let globalKeyboardHandler: ((e: KeyboardEvent) => void) | null = null

  const getStorageKey = () => {
    const baseKey = options.storageKey ?? 'onboarding_tour'
    const userId = userStore.user?.id ?? 'guest'
    const role = userStore.user?.role ?? 'user'
    return `${baseKey}_${userId}_${role}_${storageVersion}`
  }

  const hasSeen = () => {
    return localStorage.getItem(getStorageKey()) === 'true'
  }

  const markAsSeen = () => {
    localStorage.setItem(getStorageKey(), 'true')
  }

  const clearSeen = () => {
    localStorage.removeItem(getStorageKey())
  }

  const markTeamGuideAsSeen = () => {
    const userId = userStore.user?.id ?? 'guest'
    localStorage.setItem(`team_guide_${userId}_${teamStorageVersion}`, 'true')
  }

  /**
   * 检查元素是否存在，如果不存在则重试
   */
  const ensureElement = async (selector: string, timeout = 5000): Promise<boolean> => {
    const startTime = Date.now()
    while (Date.now() - startTime < timeout) {
      const element = document.querySelector(selector)
      if (element && element.getBoundingClientRect().height > 0) {
        return true
      }
      await new Promise((resolve) => setTimeout(resolve, 150))
    }
    return false
  }

  const startTour = async (startIndex = 0, flow: 'default' | 'team' = 'default', teamOptions?: TeamGuideOptions) => {
    // 动态获取当前用户角色和步骤
    const isAdmin = userStore.user?.role === 'admin'
    const isSimpleMode = userStore.isSimpleMode
    const steps: RoutedDriveStep[] = flow === 'team'
      ? getTeamSteps(t, teamOptions?.isOwner ?? false, teamOptions?.hasTeam ?? false)
      : (isAdmin ? getAdminSteps(t, isSimpleMode) : getUserSteps(t))
    const driverSteps = steps.map(({ route: _route, ...step }) => step)
    const markCurrentTourAsSeen = flow === 'team' ? markTeamGuideAsSeen : markAsSeen

    // 确保 DOM 就绪
    await nextTick()

    // 如果指定了起始步骤，确保元素可见
    const currentStep = steps[startIndex]
    if (currentStep?.element && typeof currentStep.element === 'string') {
      await ensureElement(currentStep.element, TIMING.ELEMENT_TIMEOUT_MS)
    }

    if (driverInstance) {
      driverInstance.destroy()
    }

    // 创建新的 driver 实例并存储到 store
    driverInstance = driver({
      showProgress: true,
      steps: driverSteps,
      animate: true,
      allowClose: false, // 禁止点击遮罩关闭
      stagePadding: 4,
      popoverClass: 'theme-tour-popover',
      nextBtnText: t('common.next'),
      prevBtnText: t('common.back'),
      doneBtnText: t('common.confirm'),

      // 导航处理
      onNextClick: async (_el, _step, { config, state }) => {
        // 如果是最后一步，点击则是"完成"
        if (state.activeIndex === (config.steps?.length ?? 0) - 1) {
          markCurrentTourAsSeen()
          driverInstance?.destroy()
          onboardingStore.setDriverInstance(null)
        } else {
          // 注意：交互式步骤通常隐藏 Next 按钮，此处逻辑为防御性编程
          const currentIndex = state.activeIndex ?? 0
          const currentStep = steps[currentIndex]

          if (currentStep && isInteractiveStep(currentStep) && currentStep.element) {
            const targetElement = typeof currentStep.element === 'string'
              ? document.querySelector(currentStep.element) as HTMLElement
              : currentStep.element as HTMLElement

            if (targetElement) {
              const isClickable = !['INPUT', 'TEXTAREA', 'SELECT'].includes(targetElement.tagName)
              if (isClickable) {
                targetElement.click()
                return
              }
            }
          }
          await moveToStep(currentIndex + 1)
        }
      },
      onPrevClick: async (_el, _step, { state }) => {
        await moveToStep((state.activeIndex ?? 0) - 1)
      },
      onCloseClick: () => {
        markCurrentTourAsSeen()
        driverInstance?.destroy()
        onboardingStore.setDriverInstance(null)
      },

      // 渲染时只补充交互步骤提示，底部保留 Driver.js 原生操作按钮。
      onPopoverRender: (popover, { state }) => {
        try {
          const currentStep = steps[state.activeIndex ?? 0]

          if (currentStep && isInteractiveStep(currentStep) && popover.description) {
            const hintClass = 'driver-popover-description-hint'
            if (!popover.description.querySelector(`.${hintClass}`)) {
              const hint = document.createElement('div')
              hint.className = `${hintClass} mt-2 text-xs text-gray-500 flex items-center gap-1`

              const iconSpan = document.createElement('span')
              iconSpan.className = 'i-mdi-keyboard-return mr-1'

              const textNode = document.createTextNode(
                t('onboarding.interactiveHint', 'Press Enter or Click to continue'),
              )

              hint.appendChild(iconSpan)
              hint.appendChild(textNode)
              popover.description.appendChild(hint)
            }
          }
        } catch (e) {
          console.error('Onboarding Tour Render Error:', e)
        }
      },

      // 步骤高亮时触发
      onHighlightStarted: async (element, step) => {
        // 清理之前的监听器
        cleanupClickListener()

        // 尝试等待元素
        if (!element && step.element && typeof step.element === 'string') {
           const exists = await ensureElement(step.element, 8000)
           if (!exists) {
             console.warn(`Tour element not found after 8s: ${step.element}`)
             return
           }
           element = document.querySelector(step.element) as HTMLElement
        }

        // 折叠或分页的表单先展开目标所在区域，再刷新引导定位。
        if (element) {
          element.dispatchEvent(new Event('onboarding-reveal', { bubbles: true }))
          await nextTick()
          driverInstance?.refresh()
        }

        if (isInteractiveStep(step) && element) {
          const htmlElement = element as HTMLElement

          // Check if this is a submit button - if so, don't bind auto-advance listeners
          // Let business code (e.g., handleCreateGroup) manually call nextStep after success
          const isSubmitButton = htmlElement.getAttribute('type') === 'submit' ||
                                (htmlElement.tagName === 'BUTTON' && htmlElement.closest('form'))

          if (isSubmitButton) {
            return // Don't bind any click listeners for submit buttons
          }

          const originalTabIndex = htmlElement.getAttribute('tabindex')
          if (!htmlElement.isContentEditable && htmlElement.tabIndex === -1) {
             htmlElement.setAttribute('tabindex', '0')
          }

          // Enhanced Select component detection - check both children and self
          const isSelectComponent = htmlElement.querySelector('.select-trigger') !== null ||
                                    htmlElement.classList.contains('select-trigger')

          // Select dropdowns are teleported to <body>, so click events on options
          // won't bubble through this element. Skip auto-advance for Select components.
          // Users navigate using Next/Previous buttons after making their selection.
          if (isSelectComponent) {
            return
          }

          // Single-execution protection flag
          let hasExecuted = false

          // Capture the step index when binding the handler
          const boundStepIndex = driverInstance?.getActiveIndex() ?? 0

          const clickHandler = async () => {
            // Prevent duplicate execution
            if (hasExecuted) {
              return
            }
            hasExecuted = true

            // Wait before advancing to allow user to see the result of their action
            await new Promise(resolve => setTimeout(resolve, TIMING.INTERACTIVE_WAIT_MS))

            // Verify driver is still active and not destroyed
            if (!driverInstance || !driverInstance.isActive()) {
              return
            }

            // Check if we're still on the same step - abort if step changed during wait
            const currentIndex = driverInstance.getActiveIndex() ?? 0
            if (currentIndex !== boundStepIndex) {
              return
            }

            // Final check before moving
            if (driverInstance && driverInstance.isActive()) {
              await moveToStep(currentIndex + 1)
            }
          }

          // For input fields, advance on input/change events instead of click
          const isInputField = ['INPUT', 'TEXTAREA', 'SELECT'].includes(htmlElement.tagName)

          if (isInputField) {
            const inputHandler = () => {
              // Remove listener after first input
              htmlElement.removeEventListener('input', inputHandler)
              htmlElement.removeEventListener('change', inputHandler)
              clickHandler()
            }

            htmlElement.addEventListener('input', inputHandler)
            htmlElement.addEventListener('change', inputHandler)

            currentClickListener = {
              element: htmlElement,
              handler: inputHandler,
              originalTabIndex,
              eventTypes: ['input', 'change']
            }
          } else {
            const keyHandler = (e: KeyboardEvent) => {
               if (['Enter', ' '].includes(e.key)) {
                  e.preventDefault()
                  clickHandler()
               }
            }

            htmlElement.addEventListener('click', clickHandler, { once: true })
            htmlElement.addEventListener('keydown', keyHandler)

            currentClickListener = {
              element: htmlElement,
              handler: clickHandler as () => void,
              keyHandler,
              originalTabIndex,
              eventTypes: ['click']
            }
          }
        }
      },

      onDestroyed: () => {
        cleanupClickListener()
        // 清理全局监听器 (由此处唯一管理)
        if (globalKeyboardHandler) {
          document.removeEventListener('keydown', globalKeyboardHandler, { capture: true })
          globalKeyboardHandler = null
        }
        onboardingStore.setDriverInstance(null)
      }
    })

    onboardingStore.setDriverInstance(driverInstance)

    // 添加全局键盘监听器
    globalKeyboardHandler = (e: KeyboardEvent) => {
      if (!driverInstance?.isActive()) return

      if (e.key === 'Escape') {
        e.preventDefault()
        e.stopPropagation()
        markCurrentTourAsSeen()
        driverInstance.destroy()
        onboardingStore.setDriverInstance(null)
        return
      }

      if (e.key === 'ArrowRight') {
        const target = e.target as HTMLElement
        // 允许在输入框中使用方向键
        if (['INPUT', 'TEXTAREA'].includes(target?.tagName)) {
           return
        }

        e.preventDefault()
        e.stopPropagation()

        // 对于交互式步骤，箭头键应该触发交互而非跳过
        const currentIndex = driverInstance!.getActiveIndex() ?? 0
        const currentStep = steps[currentIndex]

        if (currentStep && isInteractiveStep(currentStep) && currentStep.element) {
          const targetElement = typeof currentStep.element === 'string'
            ? document.querySelector(currentStep.element) as HTMLElement
            : currentStep.element as HTMLElement

          if (targetElement) {
            // 对于非输入类元素，提示用户需要点击或按Enter
            const isClickable = !['INPUT', 'TEXTAREA', 'SELECT'].includes(targetElement.tagName)
            if (isClickable) {
              // 不自动触发，只是停留提示
              return
            }
          }
        }

        // 非交互式步骤才允许箭头键翻页
        void moveToStep((driverInstance!.getActiveIndex() ?? 0) + 1)
      }
      else if (e.key === 'Enter') {
        const target = e.target as HTMLElement
        // 允许在输入框中使用回车
        if (['INPUT', 'TEXTAREA'].includes(target?.tagName)) {
           return
        }

        e.preventDefault()
        e.stopPropagation()

        // 回车键处理交互式步骤
        const currentIndex = driverInstance!.getActiveIndex() ?? 0
        const currentStep = steps[currentIndex]

        if (currentStep && isInteractiveStep(currentStep) && currentStep.element) {
          const targetElement = typeof currentStep.element === 'string'
            ? document.querySelector(currentStep.element) as HTMLElement
            : currentStep.element as HTMLElement

          if (targetElement) {
            const isClickable = !['INPUT', 'TEXTAREA', 'SELECT'].includes(targetElement.tagName)
            if (isClickable) {
              targetElement.click()
              return
            }
          }
        }
        void moveToStep((driverInstance!.getActiveIndex() ?? 0) + 1)
      }
      else if (e.key === 'ArrowLeft') {
        const target = e.target as HTMLElement
        // 允许在输入框中使用方向键
        if (['INPUT', 'TEXTAREA', 'SELECT'].includes(target?.tagName) || target?.isContentEditable) {
           return
        }

        e.preventDefault()
        e.stopPropagation()
        void moveToStep((driverInstance.getActiveIndex() ?? 0) - 1)
      }
    }

    document.addEventListener('keydown', globalKeyboardHandler, { capture: true })
    driverInstance.drive(startIndex)

    /**
     * 团队导览跨页面时先完成路由切换，再定位目标元素。
     */
    async function moveToStep(targetIndex: number): Promise<void> {
      const targetStep = steps[targetIndex]
      if (!targetStep || !driverInstance?.isActive()) return

      if (targetStep.route) {
        const targetRoute = router.resolve(targetStep.route)
        if (router.currentRoute.value.fullPath !== targetRoute.fullPath) {
          await router.push(targetStep.route)
          await nextTick()
        }
      }

      if (targetStep.element && typeof targetStep.element === 'string') {
        const exists = await ensureElement(targetStep.element, TIMING.ELEMENT_TIMEOUT_MS)
        if (!exists) {
          console.warn(`Onboarding: Target step element not found: ${targetStep.element}`)
          return
        }
      }

      if (driverInstance?.isActive()) driverInstance.moveTo(targetIndex)
    }
  }

  const nextStep = async (delay = 300) => {
    if (!driverInstance?.isActive()) return
    if (delay > 0) {
      await new Promise(resolve => setTimeout(resolve, delay))
    }
    driverInstance.moveNext()
  }

  const isCurrentStep = (elementSelector: string): boolean => {
    if (!driverInstance?.isActive()) return false
    const activeElement = driverInstance.getActiveElement()
    return activeElement?.matches(elementSelector) ?? false
  }

  const replayTour = () => {
    clearSeen()
    void startTour()
  }

  const startTeamTour = (teamOptions: TeamGuideOptions) => {
    void startTour(0, 'team', teamOptions)
  }

  onMounted(async () => {
    onboardingStore.setControlMethods({
      nextStep,
      isCurrentStep
    })

    if (onboardingStore.isDriverActive()) {
      driverInstance = onboardingStore.getDriverInstance()
      return
    }

    // 简易模式下禁用新手引导
    if (userStore.isSimpleMode) {
      return
    }

    // 只在管理员+标准模式下自动启动
    const isAdmin = userStore.user?.role === 'admin'
    if (!isAdmin) {
      return
    }

    if (!options.autoStart || hasSeen()) return
    autoStartTimer = setTimeout(() => {
      void startTour()
    }, TIMING.AUTO_START_DELAY_MS)
  })

  onUnmounted(() => {
    if (autoStartTimer) {
      clearTimeout(autoStartTimer)
      autoStartTimer = null
    }
    // 关键修复：不再此处清理 globalKeyboardHandler，交由 driver.onDestroyed 管理
    onboardingStore.clearControlMethods()
  })

  return {
    startTour,
    replayTour,
    startTeamTour,
    nextStep,
    isCurrentStep,
    hasSeen,
    markAsSeen,
    clearSeen
  }
}
