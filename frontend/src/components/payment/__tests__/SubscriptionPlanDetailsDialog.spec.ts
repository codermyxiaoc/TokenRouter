import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import en from '@/i18n/locales/en'
import zh from '@/i18n/locales/zh'
import type { SubscriptionPlan, SubscriptionPlanGroupPreview } from '@/types/payment'
import SubscriptionPlanDetailsDialog from '../SubscriptionPlanDetailsDialog.vue'

// 直接用中英文源码词条验证插值和复数展示。
vi.mock('vue-i18n', () => import('../../../../node_modules/vue-i18n/dist/vue-i18n.mjs'))
const getPlanGroups = vi.hoisted(() => vi.fn())
vi.mock('@/api/payment', () => ({ paymentAPI: { getPlanGroups } }))

enableAutoUnmount(afterEach)
beforeEach(() => getPlanGroups.mockReset())

function plan(id: number): SubscriptionPlan {
  return {
    id,
    name: `Plan ${id}`,
    description: 'Full plan description',
    price: 10,
    validity_days: 30,
    validity_unit: 'day',
    for_sale: true,
    sort_order: 1,
    features: [],
  }
}

function mountDialog(selected: SubscriptionPlan | null, locale = 'zh') {
  return mount(SubscriptionPlanDetailsDialog, {
    props: { plan: selected },
    global: {
      plugins: [createI18n({ legacy: false, locale, messages: { zh, en } })],
      stubs: { Teleport: true, Transition: false, ProviderIcon: true, Icon: true },
    },
  })
}

const available: SubscriptionPlanGroupPreview = {
  groups_restricted: true,
  groups: [
    { id: 1, name: 'Alpha', platform: 'openai', rate_multiplier: 0, models: ['gpt-6.2-sol'] },
    { id: 2, name: 'Beta', platform: 'gemini', rate_multiplier: 2, models: [] },
  ],
}

describe('购买前套餐详情', () => {
  it('只在打开时请求所选套餐，按组列出模型和真实倍率', async () => {
    getPlanGroups.mockResolvedValue({ data: available })
    const wrapper = mountDialog(null)
    expect(getPlanGroups).not.toHaveBeenCalled()
    await wrapper.setProps({ plan: plan(7) })
    await flushPromises()

    expect(getPlanGroups).toHaveBeenCalledWith(7, expect.any(AbortSignal))
    expect(wrapper.get('h3').text()).toContain('Plan 7')
    expect(wrapper.text()).toContain(zh.payment.planPreview.restricted)
    const groups = wrapper.findAll('details')
    expect(groups[0].attributes('open')).toBeDefined()
    expect(groups[0].get('summary').text()).toContain('Alpha')
    expect(groups[0].get('summary').text()).toContain('0x')
    expect(groups[0].get('li').text()).toBe('gpt-6.2-sol')
    expect(groups[1].get('summary').text()).toContain('2x')
    expect(groups[1].text()).toContain(zh.payment.planPreview.noModels)
  })

  it('失败后可重试，不限组套餐也展示当前无可用分组的状态', async () => {
    getPlanGroups.mockRejectedValueOnce(new Error('offline'))
      .mockResolvedValueOnce({ data: { groups_restricted: false, groups: [] } })
    const wrapper = mountDialog(plan(3), 'en')
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toBe(en.payment.planPreview.loadFailed)
    await wrapper.get('.modal-body button').trigger('click')
    await flushPromises()
    expect(getPlanGroups).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain(en.payment.planPreview.unrestricted)
    expect(wrapper.text()).toContain(en.payment.planPreview.noGroups)
  })

  it('切换套餐后忽略上一套餐延迟返回的模型', async () => {
    let finishFirst!: (response: { data: SubscriptionPlanGroupPreview }) => void
    getPlanGroups.mockImplementationOnce(() => new Promise(resolve => { finishFirst = resolve }))
      .mockResolvedValueOnce({ data: { groups_restricted: false, groups: [] } })
    const wrapper = mountDialog(plan(1))
    const signal = getPlanGroups.mock.calls[0][1] as AbortSignal
    expect(wrapper.get('[role="status"]').text()).toBe(zh.common.loading)
    await wrapper.setProps({ plan: plan(2) })
    await flushPromises()
    expect(signal.aborted).toBe(true)
    finishFirst({ data: available })
    await flushPromises()
    expect(wrapper.text()).toContain('Plan 2')
    expect(wrapper.text()).not.toContain('Alpha')
  })
})
