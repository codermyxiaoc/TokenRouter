import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import AdminOrderTable from '../AdminOrderTable.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

vi.mock('@/composables/useBalanceDisplay', () => ({
  useBalanceDisplay: () => ({ formatBalanceAmount: (amount: number) => String(amount) }),
}))

describe('AdminOrderTable', () => {
  it('使用自研选择框筛选站内余额付款，并保留订阅订单类型', async () => {
    const wrapper = mount(AdminOrderTable, {
      props: { orders: [], loading: false, page: 1, pageSize: 20, total: 0 },
      global: { stubs: { DataTable: true, Pagination: true, Icon: true, Teleport: true } },
    })

    await wrapper.findAll('.select-trigger')[2].trigger('click')
    const subscriptionOption = wrapper.findAll('[role="option"]').find((option) => option.text() === 'payment.admin.subscriptionOrder')
    await subscriptionOption!.trigger('click')

    await wrapper.findAll('.select-trigger')[1].trigger('click')
    const balanceOption = wrapper.findAll('[role="option"]').find((option) => option.text() === 'payment.methods.balance')
    expect(balanceOption).toBeDefined()
    await balanceOption!.trigger('click')

    // 付款方式和充值/订阅类型是独立字段，余额购买应查询订阅订单。
    expect(wrapper.emitted('filter')?.at(-1)).toEqual([{
      keyword: undefined,
      status: undefined,
      payment_type: 'balance',
      order_type: 'subscription',
    }])
    wrapper.unmount()
  })
})
