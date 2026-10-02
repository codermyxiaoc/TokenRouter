import { describe, expect, it } from 'vitest'
import type { MarketplaceModelPricing, MarketplaceVideoPrice } from '@/types'
import { videoPricingEntries, videoPricingRows } from '../marketplaceVideoPricing'

const t = (key: string) => key
const priced = (rows: MarketplaceVideoPrice[], mode: MarketplaceModelPricing['pricing_mode'] = 'video_token'): MarketplaceModelPricing => ({
  price_status: 'priced', pricing_mode: mode, video_prices: rows,
})

describe('统一视频分辨率价格展示', () => {
  // 旧服务的双价响应只能展示一个档位，零价不能被参考视频行或数组顺序覆盖。
  it.each([
    ['video', 'second'], ['video_token', 'million_tokens'], ['video_per_request', 'request'],
  ] as const)('deduplicates legacy %s rows without a reference-video condition', (mode, unit) => {
    const base: MarketplaceVideoPrice = { resolution: '720p', has_reference_video: false, price: 0, unit,
      video_token_prepay: null, video_image_input_pricing: { free_images: 5, price: 0.15 } }
    const reference: MarketplaceVideoPrice = { resolution: '720P', has_reference_video: true, price: 20, unit,
      video_token_prepay: { price_per_second: 0.9 }, video_image_input_pricing: { free_images: 0, price: 9 } }
    for (const rows of [[base, reference], [reference, base]]) {
      const entries = videoPricingEntries(priced(rows, mode), t)
      expect(entries).toHaveLength(1)
      expect(entries[0]).toMatchObject({ key: '720p', label: '720p', price: 0, unit,
        video_token_prepay: null, video_image_input_pricing: { free_images: 5, price: 0.15 } })
      expect(entries[0]!.has_reference_video).toBeUndefined()
      expect(videoPricingRows(priced(rows, mode), t, value => `$${value}`)).toHaveLength(1)
    }
  })

  it('keeps a reference-only legacy price and new unified rows as resolution prices', () => {
    const entries = videoPricingEntries(priced([
      { resolution: '480p', has_reference_video: true, price: 8.74, unit: 'million_tokens' },
      { resolution: '768p', price: 15, unit: 'million_tokens' },
    ]), t)
    expect(entries.map(({ label, price }) => ({ label, price }))).toEqual([
      { label: '480p', price: 8.74 }, { label: '768p', price: 15 },
    ])
    expect(entries.every(entry => entry.has_reference_video === undefined)).toBe(true)
  })

  // 无效或缺失价格仍未知，不能为合并档位补零或生成免费行。
  it('omits missing and invalid prices while preserving explicit zero', () => {
    const rows = [
      { resolution: '480p', price: undefined, unit: 'second' },
      { resolution: '720p', price: null, unit: 'second' },
      { resolution: '768p', price: -1, unit: 'second' },
      { resolution: '1080p', price: Number.NaN, unit: 'second' },
      { resolution: '1440p', price: 0, unit: 'second' },
    ] as MarketplaceVideoPrice[]
    expect(videoPricingEntries(priced(rows, 'video'), t).map(({ label, price }) => ({ label, price })))
      .toEqual([{ label: '1440p', price: 0 }])
    expect(videoPricingEntries({ ...priced(rows), price_status: 'unpriced' }, t)).toEqual([])
  })

  it('keeps the separate fallback contract and fixed image surcharge unchanged', () => {
    const pricing: MarketplaceModelPricing = { ...priced([
      { resolution: '720p', price: 10, unit: 'million_tokens', video_image_input_pricing: null },
    ]), video_image_input_pricing: { free_images: 0, price: 9 }, video_fallback_pricing: {
      resolution: '', price: 0.3, unit: 'second', video_image_input_pricing: { free_images: 5, price: 0.15 },
    } }
    const entries = videoPricingEntries(pricing, t)
    expect(entries).toHaveLength(2)
    expect(entries[0]!.video_image_input_pricing).toBeNull()
    expect(entries[1]).toMatchObject({ key: 'fallback', label: 'marketplace.videoFallbackPrice', price: 0.3, unit: 'second',
      video_image_input_pricing: { free_images: 5, price: 0.15 } })
  })
})
