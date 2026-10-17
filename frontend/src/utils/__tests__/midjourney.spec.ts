import { describe, expect, it } from 'vitest'
import { isMidjourneyImageModel } from '@/utils/midjourney'
import { defaultProviderBrandOptions, providerBrandFilterKey, resolveProviderBrand } from '@/utils/providerBrand'
import { resolveModelCapabilities } from '@/utils/modelCapabilities'

describe('Midjourney 品牌与图片型号', () => {
  it.each(['MJ-v7', 'MJ-v8.1', 'MJ-v8.2', ' mj_v7 ', 'Midjourney-v8.2', 'MJ', 'mj_custom', 'MjFast', ' MJ-v8.2-chat ', 'mj-v', 'mj-v8..2', 'MJ-sora-tts-1'])(
    '%s 使用同一品牌并识别图片输出', (model) => {
      expect(isMidjourneyImageModel(model)).toBe(true)
      expect(resolveProviderBrand(model).key).toBe('midjourney')
      expect(providerBrandFilterKey(model)).toBe('brand:midjourney')
      expect(resolveModelCapabilities(model)).toEqual({ input: ['text'], output: ['image'] })
    },
  )

  it.each(['', 'prefix-mj', 'prefix-mj-v7', 'midjourney-text', 'midjourney-v', 'midjourney-v8..2', 'gpt-image-2'])(
    '%s 不新增 MJ 前缀命中', (model) => {
      expect(isMidjourneyImageModel(model)).toBe(false)
    },
  )

  it('分组品牌列表只提供一个 Midjourney 选项，图标继承主题色', () => {
    expect(defaultProviderBrandOptions.filter(option => option.value === 'Midjourney')).toEqual([
      { value: 'Midjourney', label: 'Midjourney' },
    ])
    expect(resolveProviderBrand('MJ').iconColor).toBe('currentColor')
  })

  it('明确的服务端能力仍优先于模型名称', () => {
    expect(resolveModelCapabilities('MJ-v8.2', undefined, { input: ['text', 'image'], output: ['image'] }))
      .toEqual({ input: ['text', 'image'], output: ['image'] })
  })

  it('非前缀名称不新增图片能力', () => {
    expect(resolveModelCapabilities('prefix-mj')).toEqual({ input: ['text'], output: ['text'] })
  })
})
