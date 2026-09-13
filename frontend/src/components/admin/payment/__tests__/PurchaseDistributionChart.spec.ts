import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import PurchaseDistributionChart from '../PurchaseDistributionChart.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => ({
      'payment.admin.purchaseDistribution': '购买情况',
      'payment.admin.amountShare': '金额占比',
      'payment.admin.countShare': '订单数占比',
      'payment.admin.purchaseItem': '购买项',
      'payment.admin.revenue': '收入',
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
  })

  it('renders empty state when there is no purchase data', () => {
    const wrapper = mount(PurchaseDistributionChart, {
      props: {
        items: []
      }
    })

    expect(wrapper.text()).toContain('暂无数据')
    expect(wrapper.findAll('.doughnut-chart')).toHaveLength(0)
  })
})
