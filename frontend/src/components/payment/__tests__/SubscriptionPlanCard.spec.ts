import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import type { SubscriptionPlan } from '@/types/payment'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key,
  }),
}))

vi.mock('@/composables/useBalanceDisplay', () => ({
  useBalanceDisplay: () => ({
    formatBalanceAmount: (value: number | null | undefined) => String(value ?? ''),
  }),
}))

import SubscriptionPlanCard from '../SubscriptionPlanCard.vue'

// 统一构造套餐卡片，便于覆盖平台和币种的组合展示。
const mountPlanCard = (
  groupPlatform: string,
  currency = '',
  originalPrice?: number,
  overrides: Partial<SubscriptionPlan> = {},
) =>
  mount(SubscriptionPlanCard, {
    props: {
      plan: {
        id: 1,
        group_id: 10,
        group_platform: groupPlatform,
        name: 'Pro',
        description: '',
        price: 10,
        original_price: originalPrice,
        currency,
        validity_days: 30,
        validity_unit: 'day',
        features: [],
        for_sale: true,
        sort_order: 1,
        supported_model_scopes: ['claude', 'gemini_text', 'gemini_image'],
        ...overrides,
      },
    },
  })

describe('SubscriptionPlanCard', () => {
  it('does not show Antigravity model scopes for OpenAI plans', () => {
    const text = mountPlanCard('openai').text()

    expect(text).not.toContain('Claude')
    expect(text).not.toContain('Gemini')
    expect(text).not.toContain('Imagen')
  })

  it('shows model scopes for Antigravity plans', () => {
    const text = mountPlanCard('antigravity').text()

    expect(text).toContain('Claude')
    expect(text).toContain('Gemini')
    expect(text).toContain('Imagen')
  })

  // 卡片必须同时兼容历史单数单位和管理端曾写入的复数单位。
  it('renders plural validity units instead of mislabeled days', () => {
    expect(mountPlanCard('openai', '', undefined, { validity_days: 1, validity_unit: 'months' }).text()).toContain('/ payment.perMonth')
    expect(mountPlanCard('openai', '', undefined, { validity_days: 3, validity_unit: 'months' }).text()).toContain('/ 3payment.months')
    expect(mountPlanCard('openai', '', undefined, { validity_days: 2, validity_unit: 'weeks' }).text()).toContain('/ 2payment.weeks')
    expect(mountPlanCard('openai', '', undefined, { validity_days: 30, validity_unit: 'day' }).text()).toContain('/ 30payment.days')
  })

  it('uses the configured currency symbol on current and original prices', () => {
    const text = mountPlanCard('openai', 'NZD', 20).text()

    expect(text).toContain('NZ$10NZD')
    expect(text).toContain('NZ$20NZD')
    expect(mountPlanCard('openai', 'CNY', 20).text()).toContain('¥10CNY')
    expect(mountPlanCard('openai').text()).toContain('$10')
  })

  it.each([
    ['长中文', '企业全球加速专业订阅套餐（含高级模型与优先支持）'],
    ['长英文', 'Enterprise Global Acceleration Subscription with Priority Support'],
    ['无空格长词', 'EnterpriseGlobalAccelerationSubscriptionWithPrioritySupport1234567890'],
  ])('在固定两行区域中保留完整的%s套餐标题', (_label, name) => {
    const wrapper = mountPlanCard('openai', '', undefined, { name })
    const title = wrapper.get('h3')

    expect(title.text()).toBe(name)
    expect(title.attributes('title')).toBe(name)
    expect(title.classes()).toEqual(expect.arrayContaining([
      'min-w-0',
      'h-12',
      'break-words',
      'line-clamp-2',
      '[overflow-wrap:anywhere]',
    ]))
    expect(title.classes()).not.toContain('truncate')
  })

  it('将标题、价格、描述和购买操作保持在独立的稳定区域', () => {
    const wrapper = mountPlanCard('openai', 'USD', undefined, {
      name: 'Enterprise Global Acceleration Subscription with Priority Support',
      price: 123.45,
      description: 'Includes advanced models and priority support.',
    })
    const title = wrapper.get('h3')
    const price = wrapper.findAll('span').find(node => node.text() === '123.45')

    expect(title.element.parentElement?.classList).toContain('min-w-0')
    expect(title.element.parentElement?.classList).toContain('flex-1')
    expect(price?.element.parentElement?.parentElement?.classList).toContain('shrink-0')
    expect(wrapper.get('p').text()).toBe('Includes advanced models and priority support.')
    expect(wrapper.get('button').text()).toBe('payment.subscribeNow')
  })

  it('短标题也保持卡片对齐所需的固定高度', () => {
    const wrapper = mountPlanCard('openai', '', undefined, { name: 'Pro', description: '' })
    const title = wrapper.get('h3')

    expect(title.text()).toBe('Pro')
    expect(title.attributes('title')).toBe('Pro')
    expect(title.classes()).toEqual(expect.arrayContaining(['text-base', 'font-bold', 'h-12']))
  })
})
