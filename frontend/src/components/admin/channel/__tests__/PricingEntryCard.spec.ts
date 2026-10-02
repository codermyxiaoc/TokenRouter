import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import PricingEntryCard from '../PricingEntryCard.vue'
import { createDefaultTimePricingForm, hasExplicitPricing, isValidReasoningEffortMultipliers, reasoningEffortMultipliersToAPI, type PricingFormEntry } from '../types'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (_key: string, fallback?: string) => fallback || _key,
    }),
  }
})

function makeEntry(overrides: Partial<PricingFormEntry> = {}): PricingFormEntry {
  return {
    models: [],
    billing_mode: 'token',
    price_multiplier: null,
    fast_mode_multiplier: 2,
    fast_multiplier: null,
    flex_multiplier: null,
    input_price: 1,
    output_price: 2,
    cache_write_price: null,
    cache_read_price: null,
    image_input_price: null,
    image_output_price: null,
    per_request_price: null,
    intervals: [],
    time_pricing: createDefaultTimePricingForm(),
    ...overrides,
  }
}

function mountCard(showFastModeMultiplier: boolean) {
  return mount(PricingEntryCard, {
    props: {
      entry: makeEntry(),
      platform: 'openai',
      showFastModeMultiplier,
    },
    global: {
      stubs: {
        Icon: true,
        IntervalRow: true,
        ModelTagInput: true,
        Select: {
          template: '<button data-testid="billing-mode" @click="$emit(\'update:modelValue\', \'image\')" />',
        },
      },
    },
  })
}

describe('PricingEntryCard', () => {
  // 按次沿用视频完整价卡而非普通请求价格，离开 Token 模式时不得遗留固定秒价预扣。
  it('switches Video Token pricing to per-task with fixed image fees and no Token prepayment', async () => {
    const entry = makeEntry({ billing_mode: 'video_token', video_prices: [{ resolution: '768p', has_reference_video: false, price: 2 }],
      video_fallback_price: 3, video_image_input_pricing: { free_images: 5, price: 0.15 }, video_token_prepay: { price_per_second: 0.3 } })
    const wrapper = mount(PricingEntryCard, { props: { entry, platform: 'video' }, global: { stubs: { Icon: true, IntervalRow: true, ModelTagInput: true, Select: true } } })
    wrapper.getComponent({ name: 'Select' }).vm.$emit('update:modelValue', 'video_per_request')
    await wrapper.vm.$nextTick()
    const updated = wrapper.emitted('update')!.at(-1)![0] as PricingFormEntry
    expect(updated).toMatchObject({ billing_mode: 'video_per_request', video_prices: entry.video_prices,
      video_image_input_pricing: entry.video_image_input_pricing, video_fallback_price: 3, video_token_prepay: null })
    await wrapper.setProps({ entry: updated })
    expect(wrapper.text()).toContain('admin.channels.videoPricing.perRequestUnit')
    expect(wrapper.text()).toContain('admin.channels.videoPricing.perRequestHint')
    expect(wrapper.text()).not.toContain('$/s')
    expect(wrapper.find('[data-testid="video-token-prepay"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="video-image-input-pricing"]').exists()).toBe(true)
    expect(hasExplicitPricing({ ...updated, video_prices: [], video_fallback_price: 0 })).toBe(true)
    expect(hasExplicitPricing({ ...updated, video_prices: [], video_fallback_price: null, per_request_price: 5 })).toBe(false)
    await wrapper.setProps({ platform: 'grok' })
    const options = wrapper.getComponent({ name: 'Select' }).props('options') as Array<{ value: string }>
    expect(options.map(value => value.value)).not.toContain('video_per_request')
    expect(options.map(value => value.value)).toContain('per_request')
    wrapper.unmount()
  })

  it('Video offers only video modes and preserves the matrix when models change', async () => {
    const entry = makeEntry({ billing_mode: 'video_token', input_price: null, output_price: null, video_prices: [{ resolution: '720p', has_reference_video: false, price: 15 }] })
    const wrapper = mount(PricingEntryCard, { props: { entry, platform: 'video' }, global: { stubs: { Icon: true, IntervalRow: true, ModelTagInput: true, Select: true } } })
    const options = wrapper.getComponent({ name: 'Select' }).props('options') as Array<{ value: string }>
    expect(options.map(value => value.value)).toEqual(['video_token', 'video', 'video_per_request'])
    expect(wrapper.find('[data-testid="video-pricing-matrix"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="video-image-input-pricing"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="video-fallback-price"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="video-token-prepay"]').exists()).toBe(true)
    expect(wrapper.get('[role="switch"]').attributes('aria-checked')).toBe('false')
    wrapper.getComponent({ name: 'ModelTagInput' }).vm.$emit('update:models', ['video-model'])
    await wrapper.vm.$nextTick()
    expect(wrapper.emitted('update')).toHaveLength(1)
    expect(wrapper.emitted('update')![0][0]).toMatchObject({ models: ['video-model'], video_prices: entry.video_prices })
    await wrapper.setProps({ hideVideoUserPricing: true })
    expect(wrapper.find('[data-testid="video-image-input-pricing"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="video-fallback-price"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="video-token-prepay"]').exists()).toBe(false)
    await wrapper.setProps({ hideVideoUserPricing: false, platform: 'grok' })
    expect(wrapper.find('[data-testid="video-image-input-pricing"]').exists()).toBe(false)
    wrapper.unmount()
  })

  // 兜底价可单独生效；切换计费单位时预扣不得变成隐藏的秒计费规则。
  it('accepts fallback-only pricing and clears prepayment when leaving Token video mode', async () => {
    const entry = makeEntry({ billing_mode: 'video_token', video_prices: [], video_fallback_price: 0, video_token_prepay: { price_per_second: 0.3 } })
    expect(hasExplicitPricing(entry)).toBe(true)
    expect(hasExplicitPricing({ ...entry, billing_mode: 'video' })).toBe(true)
    const wrapper = mount(PricingEntryCard, { props: { entry, platform: 'video' }, global: { stubs: { Icon: true, IntervalRow: true, ModelTagInput: true, Select: true } } })
    expect(wrapper.get<HTMLInputElement>('[data-testid="video-fallback-price"]').element.value).toBe('0')
    wrapper.getComponent({ name: 'Select' }).vm.$emit('update:modelValue', 'video')
    await wrapper.vm.$nextTick()
    const updated = wrapper.emitted('update')!.at(-1)![0] as PricingFormEntry
    expect(updated).toMatchObject({ video_fallback_price: 0, video_token_prepay: null })
    await wrapper.setProps({ entry: updated })
    expect(wrapper.find('[data-testid="video-token-prepay"]').exists()).toBe(false)
    wrapper.getComponent({ name: 'Select' }).vm.$emit('update:modelValue', 'token')
    await wrapper.vm.$nextTick()
    expect(wrapper.emitted('update')!.at(-1)![0]).toMatchObject({ video_fallback_price: null, video_token_prepay: null })
  })

  it('编辑其它档位保留旧 Max，修改或清空 Max 后不再回退到旧值', async () => {
    const wrapper = mount(PricingEntryCard, {
      props: { entry: makeEntry({ max_reasoning_effort_multiplier: 3 }), enableTierMultipliers: true },
      global: { stubs: { Icon: true, IntervalRow: true, ModelTagInput: true, Select: true } },
    })
    await wrapper.get('[data-testid="reasoning-multiplier-high"]').setValue('1.5')
    const changed = wrapper.emitted('update')?.at(-1)?.[0] as PricingFormEntry
    expect(changed).toMatchObject({ max_reasoning_effort_multiplier: 3, reasoning_effort_multipliers: { high: '1.5' } })
    await wrapper.setProps({ entry: changed })
    await wrapper.get('[data-testid="max-reasoning-effort-multiplier"]').setValue('')
    expect(wrapper.emitted('update')?.at(-1)?.[0]).toMatchObject({ max_reasoning_effort_multiplier: null, reasoning_effort_multipliers: { high: '1.5' } })
  })

  it('序列化档位排除空值并拒绝非法或非正价格', () => {
    expect(reasoningEffortMultipliersToAPI({ high: '1.5', max: '', low: null })).toEqual({ high: 1.5 })
    expect(isValidReasoningEffortMultipliers({ high: '1.5' })).toBe(true)
    for (const value of [{ high: 0 }, { high: -1 }, { high: 'abc' }, { typo: 2 }]) {
      expect(isValidReasoningEffortMultipliers(value)).toBe(false)
    }
  })

  it('仅在显式启用时展示 Fast 模式倍率输入框', () => {
    expect(mountCard(true).find('[data-testid="fast-mode-multiplier"]').exists()).toBe(true)
    expect(mountCard(false).find('[data-testid="fast-mode-multiplier"]').exists()).toBe(false)
  })

  it('切换到非 token 模式时清空 Fast 模式倍率', async () => {
    const wrapper = mountCard(true)
    await wrapper.setProps({ entry: makeEntry({ reasoning_effort_multipliers: { high: 2 }, max_reasoning_effort_multiplier: 3 }) })
    await wrapper.get('[data-testid="billing-mode"]').trigger('click')

    const updates = wrapper.emitted('update')
    expect(updates).toHaveLength(1)
    expect(updates?.[0]?.[0]).toMatchObject({
      billing_mode: 'image',
      fast_mode_multiplier: null,
      reasoning_effort_multipliers: {},
      max_reasoning_effort_multiplier: null,
      intervals: [],
      time_pricing: createDefaultTimePricingForm(),
    })
  })

  it('启用渠道层级倍率时展示 Fast 与 Flex 输入', () => {
    const wrapper = mount(PricingEntryCard, {
      props: {
        entry: makeEntry(),
        platform: 'anthropic',
        enableTierMultipliers: true,
      },
      global: { stubs: { Icon: true, IntervalRow: true, ModelTagInput: true, Select: true } },
    })
    expect(wrapper.find('[data-testid="fast-multiplier"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="flex-multiplier"]').exists()).toBe(true)
  })
})
