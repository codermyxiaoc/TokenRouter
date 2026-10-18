import { describe, expect, it, vi } from 'vitest'
import type { MarketplaceAudioPrice, MarketplaceModelPricing } from '@/types'
import zh from '@/i18n/locales/zh/misc'
import en from '@/i18n/locales/en/misc'
import { audioPricingRows } from '../marketplaceAudioPricing'

const priced = (audio_price?: MarketplaceAudioPrice | null): MarketplaceModelPricing => ({ pricing_mode: 'audio', price_status: 'priced', audio_price })
const t = (key: string) => key

describe('音频市场价格显式单位合同', () => {
  it.each([
    ['tts', 'million_characters', 'audioTTS', 'perMillionCharacters'],
    ['stt', 'hour', 'audioSTT', 'perAudioHour'],
    ['realtime', 'minute', 'audioRealtime', 'perAudioMinute'],
  ] as const)('%s 同时支持正价和显式零价，不重复乘字符或时间单位', (mode, unit, label, unitLabel) => {
    for (const price of [0, 0.02, 15]) {
      const format = vi.fn(value => `${value} 点`)
      expect(audioPricingRows(priced({ mode, unit, price }), t, format)).toEqual([{ key: `audio-${mode}`, label: `marketplace.${label}`, value: `${price} 点 marketplace.${unitLabel}` }])
      expect(format).toHaveBeenCalledTimes(1)
      expect(format).toHaveBeenCalledWith(price)
    }
  })

  it('缺失、非法或单位歧义的价格保持未知，不能回退旧 token/图片价', () => {
    const invalid = [undefined, null,
      { mode: 'tts', unit: 'million_characters', price: undefined },
      { mode: 'tts', unit: 'million_characters', price: null },
      { mode: 'tts', unit: 'million_characters', price: '15' },
      { mode: 'tts', unit: 'million_characters', price: -1 },
      { mode: 'tts', unit: 'million_characters', price: NaN },
      { mode: 'tts', unit: 'million_characters', price: Infinity },
      { mode: 'tts', unit: 'request', price: 15 },
      { mode: 'tts', unit: 'hour', price: 15 },
      { mode: 'stt', unit: 'minute', price: 15 },
      { mode: 'unknown', unit: 'million_characters', price: 15 },
      { mode: '__proto__', price: 15 },
    ] as Array<MarketplaceAudioPrice | null | undefined>
    for (const audioPrice of invalid) expect(audioPricingRows({ ...priced(audioPrice), input_price_per_token: 1, image_price_1k: 10 }, t, String)).toEqual([])
    const valid = priced({ mode: 'tts', unit: 'million_characters', price: 15 })
    expect(audioPricingRows({ ...valid, price_status: 'unpriced' }, t, String)).toEqual([])
    expect(audioPricingRows({ ...valid, pricing_mode: 'token' }, t, String)).toEqual([])
  })

  it.each([
    [zh, ['语音合成', '语音识别', '实时语音'], ['/ 百万字符', '/ 音频小时', '/ 音频分钟']],
    [en, ['Text to speech', 'Speech to text', 'Realtime audio'], ['/ million characters', '/ audio hour', '/ audio minute']],
  ])('使用本地文案显示真正的计量单位', (locale, labels, units) => {
    const translate = (key: string) => String((locale.marketplace as Record<string, unknown>)[key.split('.')[1]!])
    const prices: MarketplaceAudioPrice[] = [{ mode: 'tts', unit: 'million_characters', price: 15 }, { mode: 'stt', unit: 'hour', price: .36 }, { mode: 'realtime', unit: 'minute', price: .05 }]
    prices.forEach((price, index) => expect(audioPricingRows(priced(price), translate, String)[0]).toMatchObject({ label: labels[index], value: `${price.price} ${units[index]}` }))
  })
})
