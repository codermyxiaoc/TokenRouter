import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import BillingSubscriptionSummary from '../BillingSubscriptionSummary.vue'
import { getBillingSubscriptionsExport } from '@/utils/billingSubscriptions'
import zh from '@/i18n/locales/zh'
import en from '@/i18n/locales/en'

// 测试环境使用运行时版 i18n，直接读取真实语言包以验证键名与展示文案。
vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string) => key.split('.').reduce((value: any, part) => value?.[part], zh) as string }),
}))

function render(row: Record<string, unknown>, showEmpty = false) {
  return mount(BillingSubscriptionSummary, {
    props: { row, showEmpty },
  })
}

describe('实际扣费套餐摘要', () => {
  it('中英文语言包提供套餐列和订阅兜底的实际文案', () => {
    expect(zh.admin.usage.billingSubscriptions).toBe('扣费套餐')
    expect(en.admin.usage.billingSubscriptions).toBe('Billed plans')
    expect(en.admin.usage.billingSubscription).toBe('Subscription')
  })

  it('显示每笔实际订阅分配，用订阅ID区分同名套餐并为缺失名称兜底', () => {
    const row = { billing_subscriptions: [
      { subscription_id: 31, plan_id: 9, plan_name: '专业套餐', amount_usd: 0.1 },
      { subscription_id: 32, plan_id: 9, plan_name: '专业套餐', amount_usd: 0.2 },
      { subscription_id: 33, amount_usd: 0.01 },
    ] }
    const wrapper = render(row)
    expect(wrapper.text()).toContain('专业套餐#31')
    expect(wrapper.text()).toContain('专业套餐#32')
    expect(wrapper.text()).toContain('订阅 #33')
    expect(getBillingSubscriptionsExport(row, '订阅')).toBe('专业套餐 (#31); 专业套餐 (#32); 订阅 #33')
  })

  it('没有实际扣费条目时不从当前密钥或订阅对象猜测套餐，也不宣称未扣费', () => {
    const wrapper = render({
      subscription: { plan: { name: '不能使用的当前套餐' } },
      api_key: { preferred_subscription_id: 7 },
      billing_subscriptions: [{ subscription_id: 7, plan_name: '零费用套餐', amount_usd: 0 }],
    }, true)
    expect(wrapper.text()).toBe('—')
    expect(wrapper.find('[data-testid="billing-subscriptions"]').exists()).toBe(false)
    expect(render({}).text()).toBe('')
  })

  it('长套餐名保留完整提示且用纯文本渲染', () => {
    const planName = '长期专业套餐'.repeat(20) + '<img src=x onerror=alert(1)>'
    const wrapper = render({ billing_subscriptions: [{ subscription_id: 8, plan_name: planName, amount_usd: 0.01 }] })
    expect(wrapper.find('.truncate').text()).toBe(planName)
    expect(wrapper.find('[title]').attributes('title')).toContain(planName)
    expect(wrapper.find('img').exists()).toBe(false)
  })
})
