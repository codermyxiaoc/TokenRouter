import { mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { afterEach, describe, expect, it } from 'vitest'
import BalanceAmount from '../BalanceAmount.vue'

const originalConfig = window.__APP_CONFIG__

afterEach(() => {
  window.__APP_CONFIG__ = originalConfig
})

describe('BalanceAmount', () => {
  it('没有自定义 SVG 时使用配置的金额符号', () => {
    window.__APP_CONFIG__ = {
      ...originalConfig,
      balance_unit_symbol: '🍥',
      balance_icon_svg: ''
    }

    const wrapper = mount(BalanceAmount, {
      props: { amount: 1.2345, fractionDigits: 4 },
      global: { plugins: [createPinia()] }
    })

    expect(wrapper.text()).toBe('🍥1.2345')
    expect(wrapper.find('.balance-icon-svg').exists()).toBe(false)
  })

  it('配置自定义 SVG 时用图标替代文本金额符号', () => {
    window.__APP_CONFIG__ = {
      ...originalConfig,
      balance_unit_symbol: '🍥',
      balance_icon_svg: '<svg viewBox="0 0 16 16"><circle cx="8" cy="8" r="6" /></svg>'
    }

    const wrapper = mount(BalanceAmount, {
      props: { amount: 2.5, fractionDigits: 2 },
      global: { plugins: [createPinia()] }
    })

    expect(wrapper.text()).toBe('2.50')
    expect(wrapper.find('.balance-icon-svg').exists()).toBe(true)
  })
})
