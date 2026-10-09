import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import zh from '@/i18n/locales/zh/misc'
import en from '@/i18n/locales/en/misc'
import { formatPaymentAmount } from '../currency'
import AmountInput from '../AmountInput.vue'

const language = vi.hoisted(() => ({ value: 'zh' }))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: { amount: string }) => {
      const messages = language.value === 'zh' ? zh : en
      if (key === 'payment.rechargeBonus.payShort') {
        return messages.payment.rechargeBonus.payShort.replace('{amount}', params?.amount ?? '')
      }
      if (key === 'payment.rechargeBonus.creditedShort') {
        return messages.payment.rechargeBonus.creditedShort.replace('{amount}', params?.amount ?? '')
      }
      return key
    },
  }),
}))

vi.mock('@/composables/useBalanceDisplay', () => ({
  useBalanceDisplay: () => ({ formatBalanceAmount: (value: number) => `额度 ${value.toFixed(2)}` }),
}))

describe('AmountInput recharge quotes', () => {
  it.each(['zh', 'en'])('shows the currency discount before fees without a fixed dollar input prefix (%s)', (locale) => {
    language.value = locale
    const wrapper = mount(AmountInput, {
      props: {
        modelValue: null, amounts: [40.3], currency: 'CNY', bonusMode: 'discount',
        bonusTiers: [{ min_amount: 0, bonus_percent: 75 }],
      },
    })
    const label = wrapper.get('[data-testid="quick-amount-credited"]').text()
    expect(label).toContain(formatPaymentAmount(10.08, 'CNY'))
    expect(label).toContain(locale === 'zh' ? '未含手续费' : 'before fees')
    // 输入金额属于支付币种，不能用固定美元符号误导人民币等支付方式。
    expect(wrapper.get('input').element.parentElement?.textContent?.trim()).toBe('')
    wrapper.unmount()
  })

  it('matches tiers using entered payment amounts and emits that amount instead of discounted or converted values', async () => {
    language.value = 'zh'
    const wrapper = mount(AmountInput, {
      props: {
        modelValue: null, amounts: [40.3], currency: 'CNY', multiplier: 0.25,
        bonusTiers: [{ min_amount: 40, bonus_percent: 25 }],
      },
    })
    expect(wrapper.get('[data-testid="quick-amount-bonus-badge"]').text()).toBe('+25%')
    expect(wrapper.get('[data-testid="quick-amount-credited"]').text()).toContain('额度 12.60')
    await wrapper.get('button').trigger('click')
    expect(wrapper.emitted('update:modelValue')).toEqual([[40.3]])
    wrapper.unmount()
  })
})
