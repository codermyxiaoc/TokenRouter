import type { MarketplaceModelPricing, MarketplaceVideoPrice } from '@/types'

type Translate = (key: string) => string

export interface VideoPricingEntry extends MarketplaceVideoPrice {
  key: string
  label: string
  scoped: boolean
}

export function videoPricingUnit(unit: MarketplaceVideoPrice['unit'], t: Translate): string {
  return unit === 'million_tokens' ? '/ 1M Token' : t(unit === 'second' ? 'marketplace.perSecond' : 'marketplace.perRequest')
}

function hasUsablePrice(item: MarketplaceVideoPrice): boolean {
  return Number.isFinite(item.price) && item.price >= 0 && ['second', 'request', 'million_tokens'].includes(item.unit)
}

// 兼容旧服务的双行响应：同分辨率优先无参考视频行，只有参考视频行时保留其完整合同。
// 只合并有效单价，显式零仍有效；不能借另一行的预扣或图片附加费拼成新价格。
function unifiedResolutionPrices(items: MarketplaceVideoPrice[]): MarketplaceVideoPrice[] {
  const selected = new Map<string, MarketplaceVideoPrice>()
  for (const item of items) {
    if (!hasUsablePrice(item)) continue
    const resolution = item.resolution.trim().toLowerCase()
    if (!resolution) continue
    const previous = selected.get(resolution)
    if (!previous || (previous.has_reference_video === true && item.has_reference_video !== true)) {
      selected.set(resolution, item)
    }
  }
  return [...selected.values()]
}

// @project-doc docs/interfaces/model_catalog_and_marketplace.md#marketplace_video_pricing
// 每档位独立读取计费合同；缺字段才兼容旧服务，不能把显式关闭误当成继承。
export function videoPricingEntries(pricing: MarketplaceModelPricing, t: Translate): VideoPricingEntry[] {
  if (pricing.price_status !== 'priced' || !['video', 'video_token', 'video_per_request'].includes(pricing.pricing_mode)) return []

  const entries: VideoPricingEntry[] = []
  const append = (item: MarketplaceVideoPrice, key: string, label: string) => {
    if (!hasUsablePrice(item)) return
    const prepay = item.video_token_prepay === undefined ? pricing.video_token_prepay : item.video_token_prepay
    entries.push({
      ...item, has_reference_video: undefined, key, label,
      scoped: item.video_image_input_pricing !== undefined || item.video_token_prepay !== undefined,
      video_image_input_pricing: item.video_image_input_pricing === undefined ? pricing.video_image_input_pricing : item.video_image_input_pricing,
      video_token_prepay: item.unit === 'million_tokens' ? prepay : null,
    })
  }

  for (const item of unifiedResolutionPrices(pricing.video_prices ?? [])) {
    append(item, item.resolution.trim().toLowerCase(), item.resolution)
  }

  // 新接口的兜底合同带独立单位，不能沿用第一档位或顶层模式；null 也禁止回退旧标量。
  if (pricing.video_fallback_pricing !== undefined) {
    if (pricing.video_fallback_pricing !== null) append(pricing.video_fallback_pricing, 'fallback', t('marketplace.videoFallbackPrice'))
  } else if (pricing.video_fallback_price != null) {
    append({ resolution: '', price: pricing.video_fallback_price, unit: pricing.pricing_mode === 'video_token' ? 'million_tokens' : pricing.pricing_mode === 'video_per_request' ? 'request' : 'second' }, 'fallback', t('marketplace.videoFallbackPrice'))
  }
  return entries
}

export function videoPricingRows(pricing: MarketplaceModelPricing, t: Translate, formatPrice: (value: number) => string) {
  return videoPricingEntries(pricing, t).map((entry) => ({
    key: entry.key,
    label: entry.label,
    value: `${formatPrice(entry.price)} ${videoPricingUnit(entry.unit, t)}`,
  }))
}
