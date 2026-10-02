import type { VideoPriceEntry, VideoImageInputPricing, VideoTokenPrepay } from '@/api/admin/channels'

export interface VideoTokenPrepayForm {
  price_per_second: number | string | null
}

// 可选价格留空表示关闭，显式零价必须保留；启用预扣后则必须填写单价。
export function validVideoFallbackPrice(value: number | string | null | undefined): boolean {
  return value == null || String(value).trim() === '' || (Number.isFinite(Number(value)) && Number(value) >= 0)
}

export function videoFallbackPriceToAPI(value: number | string | null | undefined): number | null {
  if (!validVideoFallbackPrice(value)) throw new Error('Invalid video fallback price')
  return value == null || String(value).trim() === '' ? null : Number(value)
}

export function validVideoTokenPrepay(value: VideoTokenPrepayForm | null | undefined): boolean {
  return value == null || (value.price_per_second != null && String(value.price_per_second).trim() !== '' &&
    validVideoFallbackPrice(value.price_per_second))
}

export function videoTokenPrepayToAPI(value: VideoTokenPrepayForm | null | undefined): VideoTokenPrepay | null {
  if (value == null) return null
  if (!validVideoTokenPrepay(value)) throw new Error('Invalid video token prepay')
  return { price_per_second: Number(value.price_per_second) }
}

export function videoTokenPrepayFromAPI(value: VideoTokenPrepay | null | undefined): VideoTokenPrepayForm | null {
  return value == null ? null : { ...value }
}

export interface VideoImageInputPricingForm {
  free_images: number | string | null
  price: number | string | null
}

// 关闭用 null 表示；启用后必须填写完整数值，空单价不能自动变成免费。
export function validVideoImageInputPricing(value: VideoImageInputPricingForm | null | undefined): boolean {
  if (value == null) return true
  if (value.free_images == null || String(value.free_images).trim() === '' || value.price == null || String(value.price).trim() === '') return false
  const count = Number(value.free_images)
  const price = Number(value.price)
  return Number.isSafeInteger(count) && count >= 0 && Number.isFinite(price) && price >= 0
}

export function videoImageInputPricingToAPI(value: VideoImageInputPricingForm | null | undefined): VideoImageInputPricing | null {
  if (value == null) return null
  if (!validVideoImageInputPricing(value)) throw new Error('Invalid video image input pricing')
  return { free_images: Number(value.free_images), price: Number(value.price) }
}

export function videoImageInputPricingFromAPI(value: VideoImageInputPricing | null | undefined): VideoImageInputPricingForm | null {
  return value == null ? null : { ...value }
}

export interface VideoPriceFormEntry {
  resolution: string
  // 兼容旧价卡读取，编辑和保存后的单价不再包含参考视频条件。
  has_reference_video?: boolean
  price: number | string | null
}

export const normalizeVideoResolution = (value: string): string => (/k$/i.test(value.trim()) ? value.trim().toLowerCase() : value.trim().toLowerCase().replace(/p$/, '') + 'p')

const hasVideoPrice = (value: VideoPriceFormEntry) => value.price != null && String(value.price).trim() !== ''

// 旧双价合并为分辨率单价：优先无参考视频价格，只有另一行有值时保留它，空行不覆盖显式价格。
export function videoPricesFromAPI(values: VideoPriceFormEntry[] | undefined): VideoPriceFormEntry[] {
  const prices = new Map<string, VideoPriceFormEntry>()
  for (const value of values || []) {
    const resolution = normalizeVideoResolution(value.resolution)
    const previous = prices.get(resolution)
    if (!previous || (hasVideoPrice(value) && (!hasVideoPrice(previous) ||
      (previous.has_reference_video === true && value.has_reference_video !== true)))) {
      prices.set(resolution, { ...value, resolution })
    }
  }
  return [...prices.values()].map(({ resolution, price }) => ({ resolution, price }))
}

// 空价保持缺价；显式 0 必须保留，保存时统一移除旧参考视频条件。
export function videoPricesToAPI(values: VideoPriceFormEntry[] | undefined): VideoPriceEntry[] {
  return videoPricesFromAPI(values).filter(hasVideoPrice).map(value => ({
    resolution: normalizeVideoResolution(value.resolution),
    price: Number(value.price),
  }))
}

export function validVideoPrices(values: VideoPriceFormEntry[] | undefined): boolean {
  const seen = new Set<string>()
  return (values || []).every(value => {
    if (!/^(?:[1-9]\d*p?|[24]k)$/i.test(value.resolution.trim())) return false
    const key = normalizeVideoResolution(value.resolution)
    if (seen.has(key)) return false
    seen.add(key)
    if (!hasVideoPrice(value)) return true
    return Number.isFinite(Number(value.price)) && Number(value.price) >= 0
  })
}
