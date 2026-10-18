import type { MarketplaceAudioPrice, MarketplaceModelPricing } from '@/types'

type Translate = (key: string) => string

const audioModes: Record<MarketplaceAudioPrice['mode'], { unit: MarketplaceAudioPrice['unit']; label: string; unitLabel: string }> = {
  tts: { unit: 'million_characters', label: 'marketplace.audioTTS', unitLabel: 'marketplace.perMillionCharacters' },
  stt: { unit: 'hour', label: 'marketplace.audioSTT', unitLabel: 'marketplace.perAudioHour' },
  realtime: { unit: 'minute', label: 'marketplace.audioRealtime', unitLabel: 'marketplace.perAudioMinute' },
}

// 卡片预览和完整价卡共用显式单位合同；零价有效，缺价或单位不匹配不能冒充免费。
export function audioPricingRows(pricing: MarketplaceModelPricing, t: Translate, formatPrice: (value: number) => string) {
  if (pricing.pricing_mode !== 'audio' || pricing.price_status !== 'priced') return []
  const price = pricing.audio_price
  if (!price || typeof price.price !== 'number' || !Number.isFinite(price.price) || price.price < 0) return []
  if (!Object.prototype.hasOwnProperty.call(audioModes, price.mode)) return []
  const mode = audioModes[price.mode]
  if (!mode || price.unit !== mode.unit) return []
  return [{ key: `audio-${price.mode}`, label: t(mode.label), value: `${formatPrice(price.price)} ${t(mode.unitLabel)}` }]
}
