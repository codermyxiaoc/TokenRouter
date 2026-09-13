import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import TotpLoginModal from '@/components/auth/TotpLoginModal.vue'

const { showErrorMock } = vi.hoisted(() => ({
  showErrorMock: vi.fn(),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key,
  }),
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({
    showError: (...args: any[]) => showErrorMock(...args),
  }),
}))

describe('TotpLoginModal', () => {
  beforeEach(() => {
    showErrorMock.mockReset()
  })

  it('sends verification errors to toast and does not render inline red text', async () => {
    const wrapper = mount(TotpLoginModal, {
      props: {
        tempToken: 'temp-token',
        userEmailMasked: 'u***@example.com',
      },
    })

    ;(wrapper.vm as unknown as { setError: (message: string) => void }).setError('Invalid code')
    await wrapper.vm.$nextTick()

    expect(showErrorMock).toHaveBeenCalledWith('Invalid code')
    expect(wrapper.text()).not.toContain('Invalid code')
    expect(wrapper.find('.bg-red-50').exists()).toBe(false)
  })

  it('fills visible code inputs from hidden one-time-code autofill input', async () => {
    const wrapper = mount(TotpLoginModal, {
      props: {
        tempToken: 'temp-token',
      },
    })

    const hiddenInput = wrapper.get('input[autocomplete="one-time-code"][aria-hidden="true"]')
    expect(hiddenInput.attributes('data-1p-ignore')).toBe('')
    await hiddenInput.setValue('12a3456')
    await wrapper.vm.$nextTick()

    const visibleInputs = wrapper.findAll('[data-testid="totp-digit-input"]')
    expect(visibleInputs.map(input => (input.element as HTMLInputElement).value)).toEqual([
      '1',
      '2',
      '3',
      '4',
      '5',
      '6',
    ])
    expect(wrapper.emitted('verify')).toEqual([['123456']])
  })

  it('splits a complete autofilled code written to the first visible cell', async () => {
    const wrapper = mount(TotpLoginModal, {
      props: {
        tempToken: 'temp-token',
      },
    })

    const visibleInputs = wrapper.findAll('[data-testid="totp-digit-input"]')
    expect(visibleInputs[0].attributes('maxlength')).toBe('6')
    expect(visibleInputs[0].attributes('pattern')).toBe('[0-9]{1,6}')
    expect(visibleInputs[0].attributes('autocomplete')).toBe('one-time-code')
    await visibleInputs[0].setValue('654321')
    await wrapper.vm.$nextTick()

    expect(visibleInputs.map(input => (input.element as HTMLInputElement).value)).toEqual([
      '6',
      '5',
      '4',
      '3',
      '2',
      '1',
    ])
    expect(wrapper.emitted('verify')).toEqual([['654321']])
  })

  it('handles an autofill provider that only dispatches change', async () => {
    const wrapper = mount(TotpLoginModal, {
      props: {
        tempToken: 'temp-token',
      },
    })

    const visibleInputs = wrapper.findAll('[data-testid="totp-digit-input"]')
    const firstInput = visibleInputs[0].element as HTMLInputElement
    firstInput.value = '246810'
    await visibleInputs[0].trigger('change')
    await wrapper.vm.$nextTick()

    expect(visibleInputs.map(input => (input.element as HTMLInputElement).value)).toEqual([
      '2',
      '4',
      '6',
      '8',
      '1',
      '0',
    ])
    expect(wrapper.emitted('verify')).toEqual([['246810']])
  })

  it('submits once when both autofill targets receive the same code', async () => {
    const wrapper = mount(TotpLoginModal, {
      props: {
        tempToken: 'temp-token',
      },
    })

    await wrapper.get('input[autocomplete="one-time-code"][aria-hidden="true"]').setValue('123456')
    await wrapper.findAll('[data-testid="totp-digit-input"]')[0].setValue('123456')
    await wrapper.vm.$nextTick()

    expect(wrapper.emitted('verify')).toEqual([['123456']])
  })
})
