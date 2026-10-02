import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import ModelPricingPanel from '../ModelPricingPanel.vue'
import type { MarketplaceModel, MarketplaceModelPricing } from '@/types'

vi.mock('@/composables/useBalanceDisplay', () => ({
  useBalanceDisplay: () => ({
    balanceUnitName: { value: '点' },
  }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => params ? `${key} ${JSON.stringify(params)}` : key,
    }),
  }
})

// Icon 打桩时透出 name，便于断言展开/收起箭头方向。
const IconStub = {
  name: 'Icon',
  props: {
    name: { type: String, default: '' },
    size: { type: String, default: 'md' },
    strokeWidth: { type: Number, default: 1.5 },
  },
  template: '<span class="icon-stub" :data-icon="name" />',
}

function marketplaceModel(id: string, pricing: MarketplaceModelPricing): MarketplaceModel {
  return {
    id,
    display_name: id,
    pricing,
  }
}

function mountPanel(model: MarketplaceModel) {
  return mount(ModelPricingPanel, {
    props: { model },
    global: {
      stubs: {
        Icon: IconStub,
      },
    },
  })
}

const tokenPricing: MarketplaceModelPricing = {
  pricing_mode: 'token',
  price_status: 'priced',
  input_price_per_token: 0.000001,
  output_price_per_token: 0.000002,
}

const fastPricing: MarketplaceModelPricing = {
  pricing_mode: 'token',
  price_status: 'priced',
  input_price_per_token: 0.000001,
  output_price_per_token: 0.000002,
  fast_input_price_per_token: 0.000005,
  fast_output_price_per_token: 0.000006,
}

const intervalPricing: MarketplaceModelPricing = {
  pricing_mode: 'token',
  price_status: 'priced',
  context_intervals: [
    {
      min_tokens: 0,
      max_tokens: 32000,
      input_price_per_token: 0.000001,
      output_price_per_token: 0.000002,
    },
    {
      min_tokens: 32000,
      max_tokens: null,
      input_price_per_token: 0.000003,
      output_price_per_token: 0.000004,
    },
  ],
}

const imagePricing: MarketplaceModelPricing = {
  pricing_mode: 'image',
  price_status: 'priced',
  image_price_1k: 0.5,
}

const unpricedPricing: MarketplaceModelPricing = {
  pricing_mode: 'unknown',
  price_status: 'unpriced',
}

describe('ModelPricingPanel', () => {
  // 首档来源变化不能改变其它档位的单位、预扣或固定图片费说明。
  it.each(['video', 'video_token'] as const)('keeps mixed pricing scoped when the first mode is %s', async (mode) => {
    const rows: NonNullable<MarketplaceModelPricing['video_prices']> = [
      { resolution: '480p', has_reference_video: false, price: 0.5, unit: 'second', video_token_prepay: null, video_image_input_pricing: null },
      { resolution: '720p', has_reference_video: false, price: 15, unit: 'million_tokens', video_token_prepay: { price_per_second: 0.3 }, video_image_input_pricing: { free_images: 5, price: 0.15 } },
    ]
    const pricing: MarketplaceModelPricing = {
      pricing_mode: mode, price_status: 'priced', video_prices: mode === 'video' ? rows : [...rows].reverse(),
      // 旧字段刻意放入冲突值，确认新合同不会回退到首档规则。
      video_token_prepay: { price_per_second: 8 }, video_image_input_pricing: { free_images: 0, price: 9 },
      video_fallback_price: 99,
      video_fallback_pricing: { resolution: '', price: 12, unit: 'million_tokens', video_token_prepay: { price_per_second: 0 }, video_image_input_pricing: { free_images: 0, price: 0 } },
    }
    const wrapper = mountPanel(marketplaceModel('mixed-video', pricing))
    const groups = wrapper.findAll('[data-testid="video-pricing-rule-group"]')
    expect(groups).toHaveLength(2)
    const token = groups.find((group) => group.text().includes('720p'))!
    expect(token.get('[data-testid="video-pricing-rule-scope"]').text()).not.toContain('480p')
    expect(token.text()).toContain('$0.30000')
    expect(token.text()).toContain('$0.15000')
    const fallback = groups.find((group) => group.text().includes('marketplace.videoFallbackPrice'))!
    expect(fallback.text()).toContain('$0.00000')
    expect(wrapper.text()).not.toContain('$8.00000')
    expect(wrapper.text()).not.toContain('$9.00000')
    const prices = wrapper.findAll('[data-testid="pricing-rows"] > div')
    expect(prices.find((row) => row.text().includes('480p'))!.text()).toContain('marketplace.perSecond')
    expect(prices.find((row) => row.text().includes('720p'))!.text()).toContain('/ 1M Token')
    expect(prices.find((row) => row.text().includes('marketplace.videoFallbackPrice'))!.text()).toContain('12.00 点 / 1M Token')
    await wrapper.setProps({ model: marketplaceModel('mixed-video', { ...pricing,
      video_fallback_pricing: { resolution: '', price: 0.2, unit: 'second', video_token_prepay: null, video_image_input_pricing: null },
    }) })
    const secondFallback = wrapper.findAll('[data-testid="pricing-rows"] > div').find((row) => row.text().includes('marketplace.videoFallbackPrice'))!
    expect(secondFallback.text()).toContain('marketplace.perSecond')
    expect(secondFallback.text()).not.toContain('/ 1M Token')
    expect(wrapper.findAll('[data-testid="video-pricing-rule-group"]')).toHaveLength(1)
    await wrapper.setProps({ model: marketplaceModel('mixed-video', { ...pricing, video_prices: [rows[0]!], video_fallback_pricing: null }) })
    expect(wrapper.find('[data-testid="video-pricing-rule-group"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('marketplace.videoFallbackPrice')
    wrapper.unmount()
  })

  it('groups only identical rules and keeps different reference-image fees separate', () => {
    const wrapper = mountPanel(marketplaceModel('mixed-rules', {
      pricing_mode: 'video_token', price_status: 'priced', video_prices: [
        { resolution: '480p', price: 15, unit: 'million_tokens', video_token_prepay: { price_per_second: 0.3 }, video_image_input_pricing: null },
        { resolution: '720p', price: 20, unit: 'million_tokens', video_token_prepay: { price_per_second: 0.3 }, video_image_input_pricing: null },
        { resolution: '1080p', price: 30, unit: 'million_tokens', video_token_prepay: { price_per_second: 0.3 }, video_image_input_pricing: { free_images: 5, price: 0.15 } },
      ],
    }))
    const groups = wrapper.findAll('[data-testid="video-pricing-rule-group"]')
    expect(groups).toHaveLength(2)
    expect(groups[0]!.get('[data-testid="video-pricing-rule-scope"]').text()).toContain('480p / 720p')
    expect(groups[0]!.find('[data-testid="video-image-pricing-summary"]').exists()).toBe(false)
    expect(groups[1]!.text()).toContain('1080p')
    expect(groups[1]!.text()).toContain('$0.15000')
    wrapper.unmount()
  })

  // 没有矩阵也可通过兜底价展示；预扣秒价固定且明确区别于最终 Token 单价。
  it('shows fallback-only prices and a separate fixed prepayment notice', async () => {
    const pricing: MarketplaceModelPricing = { pricing_mode: 'video_token', price_status: 'priced',
      video_prices: [], video_fallback_price: 15, video_token_prepay: { price_per_second: 0.3 } }
    const wrapper = mountPanel(marketplaceModel('video-model', pricing))
    await wrapper.get('[data-testid="model-pricing-toggle"]').trigger('click')
    expect(wrapper.get('[data-testid="pricing-rows"]').text()).toContain('marketplace.videoFallbackPrice')
    expect(wrapper.get('[data-testid="pricing-rows"]').text()).toContain('/ 1M Token')
    expect(wrapper.get('[data-testid="video-token-prepay-summary"]').text()).toContain('$0.30000')
    expect(wrapper.get('[data-testid="video-token-prepay-summary"]').text()).toContain('marketplace.videoTokenPrepay.hint')
    await wrapper.setProps({ model: marketplaceModel('video-model', { ...pricing, video_fallback_price: 0, video_token_prepay: { price_per_second: 0 } }) })
    expect(wrapper.find('[data-testid="pricing-rows"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="video-token-prepay-summary"]').text()).toContain('$0.00000')
    await wrapper.setProps({ model: marketplaceModel('video-model', { ...pricing, pricing_mode: 'video' }) })
    expect(wrapper.get('[data-testid="pricing-rows"]').text()).toContain('marketplace.perSecond')
    expect(wrapper.find('[data-testid="video-token-prepay-summary"]').exists()).toBe(false)
    await wrapper.setProps({ model: marketplaceModel('video-model', { ...pricing, video_fallback_price: null, video_token_prepay: null }) })
    expect(wrapper.find('[data-testid="model-pricing-toggle"]').exists()).toBe(false)
  })
  // 附加费始终按固定美元展示，余额单位和视频价格倍率不能改写它。
  it('shows fixed reference-image rules including first-image zero pricing', async () => {
    const pricing: MarketplaceModelPricing = { pricing_mode: 'video_token', price_status: 'priced',
      video_prices: [{ resolution: '720p', price: 15, unit: 'million_tokens' }],
      video_image_input_pricing: { free_images: 2, price: 0.05 } }
    const wrapper = mountPanel(marketplaceModel('video-model', pricing))
    const summary = wrapper.get('[data-testid="video-image-pricing-summary"]')
    expect(summary.text()).toContain('marketplace.videoImageInputPricing.afterFree')
    expect(summary.text()).toContain('"count":2')
    expect(summary.text()).toContain('$0.05000')
    expect(summary.text()).not.toContain('点')
    expect(summary.text()).toContain('marketplace.videoImageInputPricing.fixedHint')
    await wrapper.setProps({ model: marketplaceModel('video-model', { ...pricing, video_image_input_pricing: { free_images: 0, price: 0 } }) })
    expect(wrapper.get('[data-testid="video-image-pricing-summary"]').text()).toContain('marketplace.videoImageInputPricing.fromFirst')
    expect(wrapper.get('[data-testid="video-image-pricing-summary"]').text()).toContain('$0.00000')
    await wrapper.setProps({ model: marketplaceModel('video-model', { ...pricing, video_image_input_pricing: null }) })
    expect(wrapper.find('[data-testid="video-image-pricing-summary"]').exists()).toBe(false)
    wrapper.unmount()
  })
  // 按次兜底无需假造分辨率，逐档价格和附加图费都必须保留请求单位。
  it.each([false, true])('shows Video per-task pricing and image fees, fallback-only=%s', async fallbackOnly => {
    const wrapper = mountPanel(marketplaceModel('video-model', { pricing_mode: 'video_per_request', price_status: 'priced',
      video_prices: fallbackOnly ? [] : [{ resolution: '768p', has_reference_video: false, price: 2, unit: 'request' }],
      video_fallback_price: 0, video_image_input_pricing: { free_images: 5, price: 0.15 } }))
    await wrapper.get('[data-testid="model-pricing-toggle"]').trigger('click')
    expect(wrapper.text()).toContain('0.0000 点 marketplace.perRequest')
    if (!fallbackOnly) expect(wrapper.text()).toContain('2.00 点 marketplace.perRequest')
    expect(wrapper.text()).not.toContain('marketplace.perSecond')
    expect(wrapper.text()).not.toContain('/ 1M Token')
    expect(wrapper.text()).not.toContain('marketplace.pricingUnavailable')
    expect(wrapper.get('[data-testid="video-image-pricing-summary"]').text()).toContain('$0.15000')
    wrapper.unmount()
  })

  it('Video Token cards show one resolution price and retain a legacy explicit zero', async () => {
    const wrapper = mountPanel(marketplaceModel('video-model', { pricing_mode: 'video_token', price_status: 'priced', video_prices: [
      { resolution: '720p', has_reference_video: false, price: 0, unit: 'million_tokens' },
      { resolution: '720p', has_reference_video: true, price: 15, unit: 'million_tokens' },
    ] }))
    await wrapper.get('[data-testid="model-pricing-toggle"]').trigger('click')
    expect(wrapper.text()).toContain('/ 1M Token')
    expect(wrapper.findAll('[data-testid="pricing-rows"] > div')).toHaveLength(1)
    expect(wrapper.get('[data-testid="pricing-rows"]').text()).toContain('0.0000 点 / 1M Token')
    expect(wrapper.text()).not.toContain('15.00 点')
    expect(wrapper.text()).not.toContain('admin.channels.videoPricing.withReference')
    expect(wrapper.text()).not.toContain('admin.channels.videoPricing.withoutReference')
    expect(wrapper.text()).not.toContain('marketplace.pricingUnavailable')
    expect(wrapper.find('[data-testid="pricing-fast-switch"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('仅 token 价卡展示已配置推理倍率，按张价格不混入该倍率', async () => {
    const wrapper = mountPanel(marketplaceModel('gpt-6-sol', { ...tokenPricing, reasoning_effort_multipliers: { high: 1.5, max: 3 } }))
    await wrapper.get('[data-testid="model-pricing-toggle"]').trigger('click')
    expect(wrapper.get('[data-testid="reasoning-effort-pricing"]').text()).toContain('1.5x')
    expect(wrapper.get('[data-testid="reasoning-effort-pricing"]').text()).toContain('3x')
    await wrapper.setProps({ model: marketplaceModel('gpt-image-2', { ...imagePricing, reasoning_effort_multipliers: { high: 1.5 } }) })
    expect(wrapper.find('[data-testid="reasoning-effort-pricing"]').exists()).toBe(false)
  })

  it('视频定价展示分辨率和真实计费单位，零价不消失', async () => {
    const wrapper = mountPanel(marketplaceModel('grok-imagine-video-1.5', {
      pricing_mode: 'video',
      price_status: 'priced',
      video_prices: [
        { resolution: '480p', price: 0, unit: 'second' },
        { resolution: '720p', price: 0.14, unit: 'second' },
        { resolution: '1080p', price: 1.5, unit: 'request' },
      ],
    }))

    await wrapper.get('[data-testid="model-pricing-toggle"]').trigger('click')
    const rows = wrapper.get('[data-testid="pricing-rows"]')
    expect(rows.text()).toContain('480p')
    expect(rows.text()).toContain('0.0000 点 marketplace.perSecond')
    expect(rows.text()).toContain('0.1400 点 marketplace.perSecond')
    expect(rows.text()).toContain('1.50 点 marketplace.perRequest')
    expect(wrapper.find('[data-testid="pricing-fast-switch"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="pricing-interval-switch"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('marketplace.pricingUnavailable')
  })

  it('无定价模型不渲染展开入口', () => {
    const wrapper = mountPanel(marketplaceModel('m1', unpricedPricing))

    expect(wrapper.find('[data-testid="model-pricing-toggle"]').exists()).toBe(false)
  })

  it('点击触发条展开收起面板，右下角箭头同步切换方向', async () => {
    const wrapper = mountPanel(marketplaceModel('m1', tokenPricing))
    const toggle = wrapper.get('[data-testid="model-pricing-toggle"]')
    const drawer = wrapper.find('.grid')

    expect(toggle.attributes('aria-expanded')).toBe('false')
    expect(toggle.get('.icon-stub[data-icon="chevronDown"]').exists()).toBe(true)
    expect(drawer.classes()).toContain('grid-rows-[0fr]')

    await toggle.trigger('click')
    expect(toggle.attributes('aria-expanded')).toBe('true')
    expect(toggle.get('.icon-stub[data-icon="chevronUp"]').exists()).toBe(true)
    expect(wrapper.find('.grid').classes()).toContain('grid-rows-[1fr]')

    await toggle.trigger('click')
    expect(toggle.attributes('aria-expanded')).toBe('false')
    expect(wrapper.get('[data-testid="model-pricing-toggle"]').get('.icon-stub[data-icon="chevronDown"]').exists()).toBe(true)
  })

  it('完整定价单列展示，标签与价格都不换行', async () => {
    const wrapper = mountPanel(marketplaceModel('m1', tokenPricing))

    await wrapper.get('[data-testid="model-pricing-toggle"]').trigger('click')

    const rows = wrapper.get('[data-testid="pricing-rows"]')
    expect(wrapper.find('.md\\:grid-cols-2').exists()).toBe(false)
    expect(rows.findAll('.whitespace-nowrap').length).toBeGreaterThan(0)
  })

  it('标准价格行展示全部计费项', async () => {
    const wrapper = mountPanel(marketplaceModel('m1', tokenPricing))

    await wrapper.get('[data-testid="model-pricing-toggle"]').trigger('click')

    const labels = wrapper.get('[data-testid="pricing-rows"]').findAll('span:first-child').map((el) => el.text())
    expect(labels).toEqual(['marketplace.input', 'marketplace.output'])
  })

  it('展示独立的 1h 缓存写入价格', async () => {
    const wrapper = mountPanel(marketplaceModel('m1', {
      ...tokenPricing,
      cache_write_price_per_token: 0.000003,
      cache_write_1h_price_per_token: 0.000006,
    }))

    await wrapper.get('[data-testid="model-pricing-toggle"]').trigger('click')

    const labels = wrapper.get('[data-testid="pricing-rows"]').findAll('span:first-child').map((el) => el.text())
    expect(labels).toContain('marketplace.cacheWrite1h')
    expect(wrapper.text()).toContain('6.00')
  })

  it('存在 fast mode 计价时展示切换，切换后只显示 fast 价格行', async () => {
    const wrapper = mountPanel(marketplaceModel('m1', fastPricing))

    await wrapper.get('[data-testid="model-pricing-toggle"]').trigger('click')
    expect(wrapper.text()).toContain('marketplace.input')

    const fastSwitch = wrapper.get('[data-testid="pricing-fast-switch"]')
    await fastSwitch.findAll('button')[1].trigger('click')

    const labels = wrapper.get('[data-testid="pricing-rows"]').findAll('span:first-child').map((el) => el.text())
    expect(labels).toEqual(['marketplace.fastInput', 'marketplace.fastOutput'])
  })

  it('上下文区间模型展示区间切换，定价行随选中区间联动', async () => {
    const wrapper = mountPanel(marketplaceModel('m1', intervalPricing))

    await wrapper.get('[data-testid="model-pricing-toggle"]').trigger('click')

    const intervalSwitch = wrapper.get('[data-testid="pricing-interval-switch"]')
    const buttons = intervalSwitch.findAll('button')
    expect(buttons.map((button) => button.text())).toEqual(['0-32k', '32k+'])

    // 第一个区间输入价 0.000001/Token，即 1.00 点/百万Token。
    expect(wrapper.text()).toContain('1.00')

    await buttons[1].trigger('click')
    // 第二个区间输入价 0.000003/Token，即 3.00 点/百万Token。
    expect(wrapper.text()).toContain('3.00')
    expect(wrapper.text()).not.toContain('1.00')
  })

  it('图片模型展示分档价格且不显示 fast 切换', async () => {
    const wrapper = mountPanel(marketplaceModel('m1', imagePricing))

    await wrapper.get('[data-testid="model-pricing-toggle"]').trigger('click')

    expect(wrapper.text()).toContain('1K')
    expect(wrapper.find('[data-testid="pricing-fast-switch"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="pricing-interval-switch"]').exists()).toBe(false)
  })
})
