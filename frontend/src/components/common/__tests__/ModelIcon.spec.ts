import { mount } from '@vue/test-utils'
import ModelIcon from '../ModelIcon.vue'
import ProviderIcon from '../ProviderIcon.vue'
import { modelIconData } from '@/utils/modelIconData'

describe('ModelIcon', () => {
  it.each([
    ['OpenAI', 'gpt-5.6'],
    ['OpenAI Codex', 'codex-auto-review'],
    ['Grok', 'grok-4.5'],
    ['Kimi', 'kimi-k2.6'],
    ['Midjourney v7', 'MJ-v7'],
    ['Midjourney v8.1', 'MJ-v8.1'],
    ['Midjourney v8.2', 'MJ-v8.2'],
  ])('%s 模型图标继承主题文字色', (_provider, model) => {
    const wrapper = mount(ModelIcon, {
      props: {
        model,
        size: '28px',
      },
    })

    expect(wrapper.get('svg').attributes('width')).toBe('28px')
    expect(wrapper.get('path').attributes('fill')).toBe('currentColor')
    expect(wrapper.find('.model-icon-fallback').exists()).toBe(false)
  })

  it.each(['nano-banana-2', 'nano-banana-pro'])('%s 显示 Google Gemini 图标', (model) => {
    const wrapper = mount(ModelIcon, {
      props: { model },
    })

    expect(wrapper.find('svg').exists()).toBe(true)
    expect(wrapper.find('.model-icon-fallback').exists()).toBe(false)
  })

  it.each(['MJ', 'mj_custom', 'MjFast', ' MJ-v8.2-chat ', 'MJ-sora-tts-1'])('%s 使用 MJ 帆船图标', (model) => {
    const wrapper = mount(ModelIcon, { props: { model } })
    expect(wrapper.findAll('path').map(path => path.attributes('d'))).toEqual(modelIconData.midjourney.paths)
  })

  it('非前缀名称不新增 MJ 图标识别', () => {
    expect(mount(ModelIcon, { props: { model: 'prefix-mj' } }).find('.model-icon-fallback').exists()).toBe(true)
  })
})

describe('ProviderIcon', () => {
  it.each(['MJ', 'mj_custom', 'MjFast', ' MJ-v8.2-chat ', 'MJ-sora-tts-1'])('%s 的品牌使用 MJ 帆船图标', (brand) => {
    const wrapper = mount(ProviderIcon, { props: { brand } })
    expect(wrapper.findAll('path').map(path => path.attributes('d'))).toEqual(modelIconData.midjourney.paths)
  })
  it.each([
    ['xAI', 'xAI'],
    ['Moonshot', 'Kimi'],
    ['Midjourney', 'Midjourney'],
    ['Midjourney 模型', 'MJ-v8.2'],
  ])('%s 徽标继承主题文字色', (_provider, brand) => {
    const wrapper = mount(ProviderIcon, {
      props: {
        brand,
      },
    })

    expect(wrapper.get('path').attributes('fill')).toBe('currentColor')
  })

  it.each(['nano-banana-2', 'nano-banana-pro'])('%s 使用 Google 徽标而不是首字母回退', (model) => {
    const wrapper = mount(ProviderIcon, {
      props: { brand: model },
    })

    expect(wrapper.find('svg').exists()).toBe(true)
    expect(wrapper.find('.provider-icon-fallback').exists()).toBe(false)
    expect(wrapper.get('path').attributes('fill')).toBe('#4285F4')
  })
})
