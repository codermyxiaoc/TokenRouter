import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useStepUp } from '@/composables/useStepUp'
import TotpStepUpDialog from '@/components/auth/TotpStepUpDialog.vue'

const mocks = vi.hoisted(() => ({
  stepUp: vi.fn(),
  showError: vi.fn()
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({
    showError: (...args: unknown[]) => mocks.showError(...args)
  })
}))

vi.mock('@/api', () => ({
  totpAPI: {
    stepUp: (...args: unknown[]) => mocks.stepUp(...args)
  }
}))

describe('TotpStepUpDialog', () => {
  beforeEach(() => {
    mocks.stepUp.mockReset()
    mocks.showError.mockReset()
    mocks.stepUp.mockResolvedValue({ success: true })
  })

  it('splits a complete autofilled code in the visible first cell', async () => {
    // 保持请求未完成，先断言验证码已拆分到六个输入格。
    let resolveStepUp: (() => void) | undefined
    mocks.stepUp.mockImplementation(() => new Promise<void>((resolve) => {
      resolveStepUp = resolve
    }))

    const controller = useStepUp()
    controller.visible.value = true
    const wrapper = mount(TotpStepUpDialog, {
      props: { controller }
    })

    const visibleInputs = wrapper.findAll('[data-testid="totp-digit-input"]')
    expect(visibleInputs[0].attributes('autocomplete')).toBe('one-time-code')
    expect(visibleInputs[0].attributes('pattern')).toBe('[0-9]{1,6}')

    const firstInput = visibleInputs[0].element as HTMLInputElement
    firstInput.value = '135790'
    await visibleInputs[0].trigger('change')
    await vi.waitFor(() => expect(mocks.stepUp).toHaveBeenCalledWith('135790'))

    expect(visibleInputs.map(input => (input.element as HTMLInputElement).value)).toEqual([
      '1',
      '3',
      '5',
      '7',
      '9',
      '0',
    ])
    resolveStepUp?.()
    await flushPromises()
    expect(controller.visible.value).toBe(false)
  })
})
