import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import VideoModelDetails from '../VideoModelDetails.vue'
import PricingEntryCard from '../PricingEntryCard.vue'
import { copyModelDetails } from '../modelDetails'
import { createDefaultTimePricingForm, hasExplicitPricing, type PricingFormEntry } from '../types'

vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key }),
}))

describe('Video 模型详情编辑', () => {
  it('各模型独立开关，关闭保留草稿，重置恢复渠道继承', async () => {
    const wrapper = mount(VideoModelDetails, { props: {
      models: ['seedance', 'kling'], scope: 'group',
      modelValue: { seedance: { enabled: true, description: '480p，5–30 秒' } },
    } })
    const save = async () => wrapper.setProps({ modelValue: wrapper.emitted('update:modelValue')!.at(-1)![0] as any })
    expect(wrapper.findAll('textarea')).toHaveLength(1)
    await wrapper.get('[data-model-detail="kling"] [role="switch"]').trigger('click')
    await save()
    await wrapper.get('[data-model-detail="kling"] textarea').setValue('1080p，最多 10 张图')
    await save()
    expect(wrapper.props('modelValue')).toEqual({
      seedance: { enabled: true, description: '480p，5–30 秒' },
      kling: { enabled: true, description: '1080p，最多 10 张图' },
    })
    await wrapper.get('[data-model-detail="seedance"] [role="switch"]').trigger('click')
    await save()
    expect(wrapper.props('modelValue')?.seedance).toEqual({ enabled: false, description: '480p，5–30 秒' })
    expect(wrapper.find('[data-model-detail="seedance"] textarea').exists()).toBe(false)
    await wrapper.get('[data-model-detail="seedance"] [role="switch"]').trigger('click')
    await save()
    expect(wrapper.get<HTMLTextAreaElement>('[data-model-detail="seedance"] textarea').element.value).toBe('480p，5–30 秒')
    await wrapper.get('[data-model-detail="seedance"] [data-testid="inherit-model-detail"]').trigger('click')
    await save()
    expect(wrapper.props('modelValue')).toEqual({ kling: { enabled: true, description: '1080p，最多 10 张图' } })
  })

  it('说明按 Unicode 字符限制，HTML 作为普通文本编辑', async () => {
    const wrapper = mount(VideoModelDetails, { props: { models: ['model'], modelValue: { model: { enabled: true, description: '' } } } })
    const text = '😀'.repeat(2000)
    await wrapper.get('textarea').setValue(text + '<script>alert(1)</script>')
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toEqual({ model: { enabled: true, description: text } })
    expect(wrapper.get<HTMLTextAreaElement>('textarea').element.value).toBe(text)
    expect(wrapper.find('script').exists()).toBe(false)
  })

  it('只复制现有模型的独立说明，特殊键无原型继承', () => {
    const details = JSON.parse('{"__proto__":{"enabled":true,"description":"原型名模型"},"model":{"enabled":false,"description":"草稿"},"removed":{"enabled":true,"description":"删除"}}')
    const copied = copyModelDetails(['__proto__', 'constructor', 'model'], details)!
    expect(Object.keys(copied)).toEqual(['__proto__', 'model'])
    expect(copied['__proto__'].description).toBe('原型名模型')
    copied.model.description = '新草稿'
    expect(details.model.description).toBe('草稿')
    expect(copyModelDetails(['constructor'], {})).toBeUndefined()
  })

  it('删模型清理对应说明，换计费模式保留说明，旧平台和账号统计不展示', async () => {
    const entry: PricingFormEntry = {
      models: ['seedance', 'kling'], billing_mode: 'video_token', price_multiplier: null,
      fast_mode_multiplier: null, input_price: null, output_price: null, cache_write_price: null,
      cache_read_price: null, image_input_price: null, image_output_price: null, per_request_price: null,
      intervals: [], time_pricing: createDefaultTimePricingForm(),
      model_details: { seedance: { enabled: true, description: '480p' }, kling: { enabled: false, description: '720p' } },
    }
    expect(hasExplicitPricing(entry)).toBe(false)
    const wrapper = mount(PricingEntryCard, { props: { entry, platform: 'video' }, global: { stubs: { Icon: true, Select: true, IntervalRow: true, ModelTagInput: true } } })
    expect(wrapper.find('[data-testid="video-model-details"]').exists()).toBe(true)
    wrapper.getComponent({ name: 'ModelTagInput' }).vm.$emit('update:models', ['seedance'])
    await wrapper.vm.$nextTick()
    const changed = wrapper.emitted('update')!.at(-1)![0] as PricingFormEntry
    expect(changed.model_details).toEqual({ seedance: { enabled: true, description: '480p' } })
    await wrapper.setProps({ entry: changed })
    wrapper.getComponent({ name: 'Select' }).vm.$emit('update:modelValue', 'video')
    await wrapper.vm.$nextTick()
    expect((wrapper.emitted('update')!.at(-1)![0] as PricingFormEntry).model_details).toEqual(changed.model_details)
    wrapper.getComponent(VideoModelDetails).vm.$emit('update:modelValue', undefined)
    await wrapper.vm.$nextTick()
    expect(wrapper.emitted('remove')).toHaveLength(1)
    // 显式免费、倍率和附加收费配置都不属于纯说明，清除说明不得删掉价卡。
    for (const pricing of [{ video_fallback_price: 0 }, { price_multiplier: 1 }, { video_image_input_pricing: { free_images: 0, price: 0 } }]) {
      await wrapper.setProps({ entry: { ...changed, ...pricing } })
      wrapper.getComponent(VideoModelDetails).vm.$emit('update:modelValue', undefined)
      await wrapper.vm.$nextTick()
      expect(wrapper.emitted('remove')).toHaveLength(1)
      expect(wrapper.emitted('update')!.at(-1)![0]).toMatchObject({ ...pricing, model_details: undefined })
    }
    // 模型列表还有其它项时，删除最后一个带说明的模型也必须清理纯说明空卡。
    const onlyOneDetail = { ...changed, models: ['seedance', 'kling'] }
    await wrapper.setProps({ entry: onlyOneDetail })
    wrapper.getComponent({ name: 'ModelTagInput' }).vm.$emit('update:models', ['kling'])
    await wrapper.vm.$nextTick()
    expect(wrapper.emitted('remove')).toHaveLength(2)
    await wrapper.setProps({ entry: { ...onlyOneDetail, video_fallback_price: 0 } })
    wrapper.getComponent({ name: 'ModelTagInput' }).vm.$emit('update:models', ['kling'])
    await wrapper.vm.$nextTick()
    expect(wrapper.emitted('remove')).toHaveLength(2)
    expect(wrapper.emitted('update')!.at(-1)![0]).toMatchObject({ models: ['kling'], model_details: undefined, video_fallback_price: 0 })
    await wrapper.setProps({ hideVideoUserPricing: true })
    expect(wrapper.find('[data-testid="video-model-details"]').exists()).toBe(false)
    for (const platform of ['openai', 'grok', 'anthropic', 'minimax']) {
      await wrapper.setProps({ platform, hideVideoUserPricing: false })
      expect(wrapper.find('[data-testid="video-model-details"]').exists()).toBe(false)
    }
  })
})
