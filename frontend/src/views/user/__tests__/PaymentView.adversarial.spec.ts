import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, shallowMount } from '@vue/test-utils'
import PaymentView from '../PaymentView.vue'
import AmountInput from '@/components/payment/AmountInput.vue'

const mocks = vi.hoisted(() => ({ checkout: vi.fn(), createOrder: vi.fn(), showError: vi.fn() }))
vi.mock('vue-router', () => ({
  useRoute: () => ({ path: '/purchase', query: {} }),
  useRouter: () => ({ replace: vi.fn(), push: vi.fn(), resolve: vi.fn(() => ({ href: '/mock' })) }),
}))
vi.mock('vue-i18n', async () => ({ ...await vi.importActual('vue-i18n'), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ user: { id: 1, username: 'test', email: 'test@example.test', balance: 0 }, refreshUser: vi.fn() }) }))
vi.mock('@/stores/payment', () => ({ usePaymentStore: () => ({ createOrder: mocks.createOrder }) }))
vi.mock('@/stores/subscriptions', () => ({ useSubscriptionStore: () => ({ activeSubscriptions: [], fetchActiveSubscriptions: vi.fn().mockResolvedValue(undefined) }) }))
vi.mock('@/stores', () => ({ useAppStore: () => ({ showError: mocks.showError, showInfo: vi.fn(), showWarning: vi.fn() }) }))
vi.mock('@/api/payment', () => ({ paymentAPI: { getCheckoutInfo: mocks.checkout } }))

enableAutoUnmount(afterEach)
beforeEach(() => {
  mocks.createOrder.mockReset().mockRejectedValue({ message: '测试 API 已拒绝' })
  mocks.showError.mockReset()
})

async function mountCheckout(notice = '', overrides: Record<string, unknown> = {}, realInput = false) {
  // 所有 API 均为内存桩；有上限的渠道用于检测非有限输入能否绕过 UI 校验。
  mocks.checkout.mockResolvedValue({ data: {
    methods: { wxpay: { daily_limit: 0, daily_used: 0, daily_remaining: 0, single_min: 1,
      single_max: 1000, fee_fixed: 0, fee_rate: 0, currency: 'USD', available: true } },
    global_min: 1, global_max: 1000, plans: [], balance_disabled: false,
    balance_recharge_multiplier: 1, subscription_usd_to_cny_rate: 0,
    recharge_fee_rate: 0, method_fees: {}, help_text: '', help_image_url: '', stripe_publishable_key: '',
    recharge_bonus_tiers: [{ min_amount: 100, bonus_percent: 20 }],
    recharge_bonus_notice: notice,
    ...overrides,
  } })
  const wrapper = shallowMount(PaymentView, { global: { stubs: {
    AppLayout: { template: '<div><slot /></div>' }, Teleport: true, Transition: false,
    ...(realInput ? { AmountInput: false } : {}),
  } } })
  await flushPromises()
  return wrapper
}

describe('支付公告攻击字符串', () => {
  it.each([
    '<script>window.__attack=1</script>',
    '<img src=x onerror="window.__attack=1">',
    '<svg onload="window.__attack=1"></svg>',
    '<a href="javascript:alert(1)">链接</a>',
    '<a href="java&#x73;cript:alert(1)">链接</a>',
    '<a href="data:text/html,<script>alert(1)</script>">链接</a>',
    '<iframe srcdoc="<script>alert(1)</script>"></iframe>',
    '<math><mtext><table><mglyph><style><!--</style><img title="--><img src=1 onerror=alert(1)>">',
    '<svg><a xlink:href="javascript:alert(1)">链接</a></svg>',
    '[链接](javascript:alert%281%29)',
    '<form action="javascript:alert(1)"><button formaction="javascript:alert(1)">提交</button></form>',
    '<object data="data:text/html,attack"></object><embed src="javascript:alert(1)">',
  ])('清洗 %s', async (payload) => {
    const wrapper = await mountCheckout(payload)
    const notice = wrapper.find('.prose')
    if (!notice.exists()) return
    expect(notice.find('script,iframe,object,embed').exists()).toBe(false)
    for (const element of notice.element.querySelectorAll('*')) {
      for (const attribute of element.attributes) {
        expect(attribute.name).not.toMatch(/^on/i)
        if (/^(href|src|xlink:href|action|formaction)$/i.test(attribute.name)) {
          expect(attribute.value.replace(/\s/g, '')).not.toMatch(/^(javascript|vbscript|data:text\/html):/i)
        }
      }
    }
  })
})

describe('支付提交攻击数值', () => {
  it.each([NaN, -Infinity, -1, 0, 1001, Number.MAX_VALUE])('拒绝无效或超限金额 %s', async (amount) => {
    const wrapper = await mountCheckout()
    wrapper.getComponent(AmountInput).vm.$emit('update:modelValue', amount)
    await flushPromises()
    const vm = wrapper.vm as unknown as { canSubmit: boolean; handleSubmitRecharge: () => Promise<void> }
    expect(vm.canSubmit).toBe(false)
    await vm.handleSubmitRecharge()
    expect(mocks.createOrder).not.toHaveBeenCalled()
  })

  it('非有限正数不能被归零报价伪装成符合渠道限额', async () => {
    const wrapper = await mountCheckout()
    wrapper.getComponent(AmountInput).vm.$emit('update:modelValue', Infinity)
    await flushPromises()
    const vm = wrapper.vm as unknown as { canSubmit: boolean; handleSubmitRecharge: () => Promise<void> }
    await vm.handleSubmitRecharge()
    expect(mocks.createOrder).not.toHaveBeenCalled()
    expect(vm.canSubmit).toBe(false)
    expect(wrapper.text()).toContain('payment.invalidAmount')
    expect(wrapper.text()).not.toContain('payment.paymentAmount')
  })

  it('真实输入先合法再粘贴超大金额时不会提交旧金额或零元报价', async () => {
    const wrapper = await mountCheckout('', {}, true)
    const input = wrapper.getComponent(AmountInput).get('input')
    await input.setValue('100')
    expect((wrapper.vm as unknown as { canSubmit: boolean }).canSubmit).toBe(true)
    await input.setValue('9'.repeat(400))
    const vm = wrapper.vm as unknown as { canSubmit: boolean; handleSubmitRecharge: () => Promise<void> }
    expect(vm.canSubmit).toBe(false)
    expect(wrapper.find('[role="alert"]').text()).toBe('payment.invalidAmount')
    expect(wrapper.text()).not.toContain('payment.paymentAmount')
    await vm.handleSubmitRecharge()
    expect(mocks.createOrder).not.toHaveBeenCalled()
  })

  it.each([
    { balance_recharge_multiplier: Infinity },
    { balance_recharge_multiplier: NaN },
    { balance_recharge_multiplier: -Infinity },
    { balance_recharge_multiplier: Number.MAX_VALUE },
    { methods: { wxpay: { single_min: 0, single_max: 0, fee_fixed: NaN, fee_rate: 0, currency: 'USD', available: true } } },
    { methods: { wxpay: { single_min: 0, single_max: 0, fee_fixed: 0, fee_rate: Number.MAX_VALUE, currency: 'USD', available: true } } },
  ])('无上限或派生金额溢出不能通过报价校验 %j', async (overrides) => {
    const wrapper = await mountCheckout('', overrides)
    wrapper.getComponent(AmountInput).vm.$emit('update:modelValue', 100)
    await flushPromises()
    const vm = wrapper.vm as unknown as { canSubmit: boolean; handleSubmitRecharge: () => Promise<void> }
    expect(vm.canSubmit).toBe(false)
    expect(wrapper.text()).toContain('payment.invalidAmount')
    await vm.handleSubmitRecharge()
    expect(mocks.createOrder).not.toHaveBeenCalled()
  })

  it.each([NaN, Infinity, Number.MAX_VALUE])('渠道无上限时仍拒绝非法金额或派生报价 %s', async (amount) => {
    const wrapper = await mountCheckout('', {
      methods: { wxpay: { single_min: 0, single_max: 0, fee_fixed: 0, fee_rate: 0, currency: 'USD', available: true } },
    })
    wrapper.getComponent(AmountInput).vm.$emit('update:modelValue', amount)
    await flushPromises()
    const vm = wrapper.vm as unknown as { canSubmit: boolean; handleSubmitRecharge: () => Promise<void> }
    expect(vm.canSubmit).toBe(false)
    await vm.handleSubmitRecharge()
    expect(mocks.createOrder).not.toHaveBeenCalled()
  })
})
