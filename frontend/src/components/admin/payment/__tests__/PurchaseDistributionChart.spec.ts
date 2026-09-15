import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import PurchaseDistributionChart from '../PurchaseDistributionChart.vue'
import Select from '@/components/common/Select.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => ({
      'payment.admin.purchaseDistribution': '购买情况',
      'payment.admin.amountShare': '金额占比',
      'payment.admin.countShare': '订单数占比',
      'payment.admin.purchaseItem': '购买项',
      'payment.admin.revenue': '支付金额',
      'payment.admin.statsCurrency': '统计币种',
      'payment.admin.orderCount': '订单数',
      'payment.admin.payAsYouGo': '按量支付',
      'payment.admin.noData': '暂无数据'
    }[key] || key)
  })
}))

vi.mock('vue-chartjs', () => ({
  Doughnut: {
    name: 'Doughnut',
    props: ['data', 'options'],
    template: '<div class="doughnut-chart"></div>'
  }
}))

vi.mock('chart.js', () => ({
  Chart: {
    register: vi.fn()
  },
  ArcElement: {},
  Tooltip: {},
  Legend: {}
}))

describe('PurchaseDistributionChart', () => {
  it('renders amount and count doughnut charts with detail rows', () => {
    const wrapper = mount(PurchaseDistributionChart, {
      props: {
        items: [
          { type: 'balance', label: 'balance', amount: 50, count: 2 },
          { type: 'subscription', label: '专业版', plan_id: 7, amount: 30, count: 1 }
        ]
      }
    })

    expect(wrapper.text()).toContain('购买情况')
    expect(wrapper.text()).toContain('金额占比')
    expect(wrapper.text()).toContain('订单数占比')
    expect(wrapper.text()).toContain('按量支付')
    expect(wrapper.text()).toContain('专业版')
    expect(wrapper.findAll('.doughnut-chart')).toHaveLength(2)
    // 历史响应未返回币种时使用默认 CNY，不能继续硬编码美元符号。
    expect(wrapper.text()).toContain('¥50.00')
    expect(wrapper.text()).not.toContain('$50.00')
    expect(wrapper.findComponent(Select).props('modelValue')).toBe('CNY')
    wrapper.unmount()
  })

  it('switches both charts and detail rows between external CNY orders and wallet USD purchases', async () => {
    const wrapper = mount(PurchaseDistributionChart, {
      props: {
        items: [
          { type: 'balance', label: 'balance', currency: 'CNY', amount: 100, count: 2 },
          { type: 'subscription', label: '专业版', plan_id: 7, currency: 'CNY', amount: 70, count: 1 },
          { type: 'subscription', label: '专业版', plan_id: 7, currency: 'USD', amount: 10, count: 3 }
        ]
      }
    })

    const charts = () => wrapper.findAllComponents({ name: 'Doughnut' })
    expect(charts()[0].props('data').datasets[0].data).toEqual([100, 70])
    expect(charts()[1].props('data').datasets[0].data).toEqual([2, 1])
    expect(wrapper.text()).toContain('¥70.00')
    expect(wrapper.text()).not.toContain('$10.00')

    // 通过项目选择框切换，验证 USD 的余额订阅订单独立展示。
    await wrapper.find('button[aria-label="统计币种"]').trigger('click')
    const usdOption = [...document.querySelectorAll<HTMLElement>('[role="option"]')]
      .find(option => option.textContent?.trim() === 'USD')
    expect(usdOption).toBeDefined()
    usdOption!.click()
    await wrapper.vm.$nextTick()

    expect(wrapper.findComponent(Select).props('modelValue')).toBe('USD')
    expect(charts()[0].props('data').datasets[0].data).toEqual([10])
    expect(charts()[1].props('data').datasets[0].data).toEqual([3])
    expect(wrapper.findAll('tbody tr')).toHaveLength(1)
    expect(wrapper.text()).toContain('$10.00')
    expect(wrapper.text()).not.toContain('¥70.00')
    expect(wrapper.text()).not.toContain('按量支付')

    const tooltip = charts()[0].props('options').plugins.tooltip.callbacks.label
    expect(tooltip({ raw: 10, label: '专业版', dataset: { data: [10] } })).toContain('$10.00 (100.0%)')
    wrapper.unmount()
  })

  it('keeps an available currency on refresh and resets when the date range no longer includes it', async () => {
    const wrapper = mount(PurchaseDistributionChart, {
      props: {
        items: [{ type: 'subscription', label: '专业版', currency: 'USD', amount: 10, count: 1 }]
      }
    })

    expect(wrapper.findComponent(Select).props('modelValue')).toBe('USD')
    await wrapper.setProps({
      items: [
        { type: 'subscription', label: '专业版', currency: 'USD', amount: 20, count: 2 },
        { type: 'balance', label: 'balance', currency: 'CNY', amount: 100, count: 1 }
      ]
    })
    expect(wrapper.findComponent(Select).props('modelValue')).toBe('USD')
    expect(wrapper.text()).toContain('$20.00')

    await wrapper.setProps({ items: [{ type: 'balance', label: 'balance', currency: 'CNY', amount: 100, count: 1 }] })
    expect(wrapper.findComponent(Select).props('modelValue')).toBe('CNY')
    expect(wrapper.text()).toContain('¥100.00')
    expect(wrapper.text()).not.toContain('$20.00')
    wrapper.unmount()
  })

  it('renders empty state when there is no purchase data', () => {
    const wrapper = mount(PurchaseDistributionChart, {
      props: {
        items: []
      }
    })

    expect(wrapper.text()).toContain('暂无数据')
    expect(wrapper.findAll('.doughnut-chart')).toHaveLength(0)
    expect(wrapper.findComponent(Select).exists()).toBe(false)
    wrapper.unmount()
  })
})
