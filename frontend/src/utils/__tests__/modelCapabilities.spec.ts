import { describe, expect, it } from 'vitest'
import { resolveModelCapabilities } from '@/utils/modelCapabilities'
import type { MarketplaceModelPricing } from '@/types'

const textPricing: MarketplaceModelPricing = {
  pricing_mode: 'token',
  price_status: 'priced',
}

const visionPricing: MarketplaceModelPricing = {
  pricing_mode: 'token',
  price_status: 'priced',
  image_input_price_per_token: 0.000003,
}

const imagePricing: MarketplaceModelPricing = {
  pricing_mode: 'image',
  price_status: 'priced',
  image_price_1k: 0.5,
}

describe('resolveModelCapabilities', () => {
  it('优先使用后端下发的能力元数据，忽略本地规则', () => {
    // gpt-image-2 按本地规则会解析为 text+image 输入，接口数据应直接覆盖。
    expect(
      resolveModelCapabilities('gpt-image-2', imagePricing, {
        input: ['text'],
        output: ['image'],
      })
    ).toEqual({
      input: ['text'],
      output: ['image'],
    })
  })

  it('接口数据按固定顺序输出并过滤非法取值', () => {
    expect(
      resolveModelCapabilities('m1', textPricing, {
        input: ['video', 'text', 'file' as never],
        output: ['audio', 'text'],
      })
    ).toEqual({
      input: ['text', 'video'],
      output: ['text', 'audio'],
    })
  })

  it('接口数据缺失或单侧为空时回退到本地规则', () => {
    // 完全缺失：走模型 ID 规则（claude 命中视觉模型）。
    expect(resolveModelCapabilities('claude-sonnet-4-5', textPricing).input).toEqual(['text', 'image'])
    // 单侧为空视为不可信数据，同样回退。
    expect(
      resolveModelCapabilities('claude-sonnet-4-5', textPricing, { input: ['text', 'image'] }).input
    ).toEqual(['text', 'image'])
  })

  it('默认按纯文本模型处理', () => {
    expect(resolveModelCapabilities('some-random-model', textPricing)).toEqual({
      input: ['text'],
      output: ['text'],
    })
  })

  it('按定价数据识别图片输入', () => {
    expect(resolveModelCapabilities('gpt-5.5', visionPricing).input).toEqual(['text', 'image'])
  })

  it('按模型 ID 识别已知的视觉模型', () => {
    expect(resolveModelCapabilities('claude-sonnet-4-5', textPricing).input).toEqual(['text', 'image'])
    expect(resolveModelCapabilities('gemini-2.5-pro', textPricing).input).toEqual(['text', 'image'])
  })

  it('按图片计费模式识别图片输出', () => {
    expect(resolveModelCapabilities('gemini-2.5-flash-image', imagePricing)).toEqual({
      input: ['text'],
      output: ['image'],
    })
  })

  it('定价缺失时按生图模型 ID 兜底识别图片输出', () => {
    expect(resolveModelCapabilities('flux-schnell', textPricing).output).toEqual(['image'])
  })

  it('识别支持图片编辑的生图模型输入包含图片', () => {
    expect(resolveModelCapabilities('gpt-image-2', imagePricing)).toEqual({
      input: ['text', 'image'],
      output: ['image'],
    })
  })

  it('纯生图模型不叠加图片输入', () => {
    const capabilities = resolveModelCapabilities('dall-e-3', textPricing)
    expect(capabilities.input).toEqual(['text'])
    expect(capabilities.output).toEqual(['image'])
  })

  it('识别音频双通的对话模型', () => {
    expect(resolveModelCapabilities('gpt-4o-audio-preview', textPricing)).toEqual({
      input: ['text', 'audio'],
      output: ['text', 'audio'],
    })
  })

  it('识别仅音频输入的语音识别模型', () => {
    expect(resolveModelCapabilities('whisper-1', textPricing)).toEqual({
      input: ['audio'],
      output: ['text'],
    })
  })

  it('识别仅音频输出的语音合成模型', () => {
    expect(resolveModelCapabilities('tts-1-hd', textPricing)).toEqual({
      input: ['text'],
      output: ['audio'],
    })
  })

  it('识别视频生成模型', () => {
    expect(resolveModelCapabilities('veo-3', textPricing).output).toEqual(['video'])
    expect(resolveModelCapabilities('sora-1', textPricing).output).toEqual(['video'])
  })

  it('音频对话模型不按 gpt-4o 模式误判图片输入', () => {
    expect(resolveModelCapabilities('gpt-4o-audio-preview', textPricing).input).not.toContain('image')
  })
})
