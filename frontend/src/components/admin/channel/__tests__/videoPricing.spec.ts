import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import VideoPricingMatrix from '../VideoPricingMatrix.vue'
import { validVideoPrices, videoPricesFromAPI, videoPricesToAPI, validVideoImageInputPricing, videoImageInputPricingToAPI, videoImageInputPricingFromAPI, validVideoFallbackPrice, videoFallbackPriceToAPI, validVideoTokenPrepay, videoTokenPrepayToAPI, videoTokenPrepayFromAPI } from '../videoPricing'
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('Video pricing matrix', () => {
  // 兜底可以留空关闭，启用预扣必须明确单价；两者都保留零价。
  it('validates fallback and fixed prepay prices without treating blanks as zero', () => {
    expect(videoFallbackPriceToAPI('')).toBeNull()
    expect(videoFallbackPriceToAPI('0')).toBe(0)
    expect(videoFallbackPriceToAPI('15')).toBe(15)
    expect(videoTokenPrepayToAPI(null)).toBeNull()
    expect(videoTokenPrepayToAPI({ price_per_second: '0' })).toEqual({ price_per_second: 0 })
    expect(videoTokenPrepayToAPI({ price_per_second: '0.3' })).toEqual({ price_per_second: 0.3 })
    const saved = { price_per_second: 0.3 }
    const form = videoTokenPrepayFromAPI(saved)!
    form.price_per_second = 0
    expect(saved.price_per_second).toBe(0.3)
    for (const price of [-1, NaN, Infinity, 'abc']) {
      expect(validVideoFallbackPrice(price)).toBe(false)
      expect(() => videoFallbackPriceToAPI(price)).toThrow()
      expect(validVideoTokenPrepay({ price_per_second: price })).toBe(false)
      expect(() => videoTokenPrepayToAPI({ price_per_second: price })).toThrow()
    }
    expect(validVideoTokenPrepay({ price_per_second: '' })).toBe(false)
    expect(validVideoTokenPrepay({ price_per_second: null })).toBe(false)
  })
  // 参考图片价格关闭与显式零价有不同含义，免费张数禁止小数和非法数值。
  it('preserves fixed image prices and validates their free allowance', () => {
    expect(videoImageInputPricingToAPI(undefined)).toBeNull()
    expect(videoImageInputPricingToAPI({ free_images: 0, price: 0 })).toEqual({ free_images: 0, price: 0 })
    expect(videoImageInputPricingToAPI({ free_images: '3', price: '0.05' })).toEqual({ free_images: 3, price: 0.05 })
    const saved = { free_images: 2, price: 0.04 }
    const form = videoImageInputPricingFromAPI(saved)!
    form.price = '0.08'
    expect(saved.price).toBe(0.04)
    for (const value of [
      { free_images: -1, price: 1 }, { free_images: 1.5, price: 1 },
      { free_images: NaN, price: 1 }, { free_images: Infinity, price: 1 },
      { free_images: 0, price: -1 }, { free_images: 0, price: NaN },
      { free_images: 0, price: Infinity }, { free_images: 0, price: '' },
      { free_images: '', price: 1 }, { free_images: 0, price: null },
    ]) {
      expect(validVideoImageInputPricing(value)).toBe(false)
      expect(() => videoImageInputPricingToAPI(value)).toThrow()
    }
  })
  // 空价与零价分别代表缺价和免费，按 Token 价格保持每百万单位。
  it('preserves explicit zero and million-token units without inventing missing prices', () => {
    expect(videoPricesToAPI([
      { resolution: '480', has_reference_video: false, price: 0 },
      { resolution: '480P', has_reference_video: true, price: '' },
      { resolution: '2K', has_reference_video: true, price: 15 },
    ])).toEqual([
      { resolution: '480p', price: 0 },
      { resolution: '2k', price: 15 },
    ])
  })

  it('rejects normalized duplicate resolutions and invalid prices', () => {
    expect(validVideoPrices([{ resolution: '480', price: 1 }, { resolution: '480P', price: 2 }])).toBe(false)
    expect(validVideoPrices([{ resolution: '2K', price: 10 }, { resolution: '4k', price: 0 }])).toBe(true)
    expect(validVideoPrices([{ resolution: '1080p', price: -1 }])).toBe(false)
  })

  // 回读旧双价时保留零价和仅参考视频有价的配置，避免空行覆盖用户价格。
  it.each([false, true])('merges legacy prices independently of row order, reversed=%s', reverse => {
    const legacy = [
      { resolution: '480', has_reference_video: false, price: 0 },
      { resolution: '480P', has_reference_video: true, price: 0 },
      { resolution: '720p', has_reference_video: false, price: '' },
      { resolution: '720', has_reference_video: true, price: 15 },
      { resolution: '2K', has_reference_video: true, price: 20 },
    ]
    const original = JSON.stringify(legacy)
    const merged = videoPricesFromAPI(reverse ? [...legacy].reverse() : legacy)
    expect(merged).toHaveLength(3)
    expect(merged).toEqual(expect.arrayContaining([
      { resolution: '480p', price: 0 }, { resolution: '720p', price: 15 }, { resolution: '2k', price: 20 },
    ]))
    expect(validVideoPrices(merged)).toBe(true)
    expect(videoPricesToAPI(merged)).toEqual(merged)
    expect(JSON.stringify(legacy)).toBe(original)
  })

  it('keeps an explicit legacy zero instead of replacing it with a reference price', () => {
    expect(videoPricesToAPI([
      { resolution: '720p', has_reference_video: true, price: 15 },
      { resolution: '720P', has_reference_video: false, price: 0 },
      { resolution: '720', price: ' ' },
    ])).toEqual([{ resolution: '720p', price: 0 }])
  })

  // 空价卡不预设分辨率，添加后的常用档位也能完全删除。
  it('starts empty and lets users add and remove a resolution', async () => {
    const wrapper = mount(VideoPricingMatrix, { props: { mode: 'video_token', modelValue: [] } })
    expect(wrapper.find('[data-testid="video-pricing-empty"]').exists()).toBe(true)
    expect(wrapper.findAll('input[type="number"]')).toHaveLength(0)
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    expect(wrapper.get('button').attributes('disabled')).toBeDefined()
    await wrapper.get('input:not([type="number"])').setValue('1080P')
    await wrapper.get('input:not([type="number"])').trigger('keydown.enter')
    const added = [{ resolution: '1080p', price: null }]
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toEqual(added)
    await wrapper.setProps({ modelValue: added })
    expect(wrapper.find('[data-testid="video-pricing-empty"]').exists()).toBe(false)
    expect(wrapper.findAll('input[type="number"]')).toHaveLength(1)
    expect(wrapper.find('[data-testid="video-price-480p"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="video-price-720p"]').exists()).toBe(false)
    await wrapper.get('button[aria-label="common.delete 1080p"]').trigger('click')
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toEqual([])
    await wrapper.setProps({ modelValue: [] })
    expect(wrapper.find('[data-testid="video-pricing-empty"]').exists()).toBe(true)
    wrapper.unmount()
  })

  // 三种计费单位共用每分辨率一个输入框，旧条件合并后编辑不再写回参考视频字段。
  it.each(['video_token', 'video', 'video_per_request'] as const)('edits one price per resolution for %s and supports custom rows', async mode => {
    const configured = [
      { resolution: '720p', has_reference_video: false, price: 8.74 },
      { resolution: '720p', has_reference_video: true, price: 8.74 },
    ]
    const wrapper = mount(VideoPricingMatrix, { props: { mode, modelValue: configured } })
    expect(wrapper.text()).toContain(mode === 'video_token' ? '$/1M Token' : mode === 'video' ? '$/s' : 'admin.channels.videoPricing.perRequestUnit')
    expect(wrapper.findAll('input[type="number"]')).toHaveLength(1)
    expect(wrapper.text()).not.toContain('admin.channels.videoPricing.withReference')
    expect(wrapper.text()).not.toContain('admin.channels.videoPricing.withoutReference')
    expect(wrapper.get<HTMLInputElement>('[data-testid="video-price-720p"]').element.value).toBe('8.74')
    await wrapper.get('[data-testid="video-price-720p"]').setValue('0')
    const edited = [{ resolution: '720p', price: '0' }]
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toEqual(edited)
    await wrapper.setProps({ modelValue: edited })
    await wrapper.get('input:not([type="number"])').setValue('2K')
    await wrapper.findAll('button').at(-1)!.trigger('click')
    const custom = { resolution: '2k', price: null }
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toEqual([...edited, custom])
    await wrapper.setProps({ mode: 'video', modelValue: [...edited, custom] })
    expect(wrapper.text()).toContain('$/s')
    await wrapper.get('button[aria-label="common.delete 720p"]').trigger('click')
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toEqual([custom])
    expect(configured[1].price).toBe(8.74)
    wrapper.unmount()
  })
})
