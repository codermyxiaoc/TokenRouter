import { afterEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, mount } from '@vue/test-utils'
import AmountInput from '../AmountInput.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/composables/useBalanceDisplay', () => ({ useBalanceDisplay: () => ({ formatBalanceAmount: String }) }))
enableAutoUnmount(afterEach)

describe('充值输入框异常文本', () => {
  it.each(['1e309', '-1', '+1', '0x10', '1.001', 'NaN', 'Infinity', '<script>alert(1)</script>', '1,000'])('文本 %s 不成为支付金额', async (value) => {
    const wrapper = mount(AmountInput, { props: { modelValue: null } })
    await wrapper.get('input').setValue(value)
    expect(wrapper.emitted('update:modelValue')).toEqual([[null]])
    expect(wrapper.get('[role="alert"]').text()).toBe('payment.invalidAmount')
    expect(wrapper.get('input').attributes('aria-invalid')).toBe('true')
  })

  it('超长十进制数字不能向父组件发出 Infinity', async () => {
    const wrapper = mount(AmountInput, { props: { modelValue: null } })
    await wrapper.get('input').setValue('9'.repeat(400))
    // 用户直接粘贴超长金额即可到达，无需篡改组件 props 或直接调用内部方法。
    for (const [amount] of wrapper.emitted('update:modelValue') ?? []) {
      expect(amount === null || Number.isFinite(amount)).toBe(true)
    }
    expect(wrapper.get('[role="alert"]').text()).toBe('payment.invalidAmount')
  })

  it.each(['9'.repeat(400), 'NaN', '1.001', '0', '.'])('合法金额改成非法文本 %s 后清除旧金额并允许修正', async (value) => {
    const wrapper = mount(AmountInput, { props: { modelValue: 100 } })
    await wrapper.get('input').setValue(value)
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([null])
    expect(wrapper.get('input').element.value).toBe(value)
    expect(wrapper.find('[role="alert"]').exists()).toBe(true)
    await wrapper.get('input').setValue('40.30')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([40.3])
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
  })
})
