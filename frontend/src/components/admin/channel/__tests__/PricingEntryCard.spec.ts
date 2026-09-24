import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import PricingEntryCard from '../PricingEntryCard.vue'
import { createDefaultTimePricingForm, isValidReasoningEffortMultipliers, reasoningEffortMultipliersToAPI, type PricingFormEntry } from '../types'

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
