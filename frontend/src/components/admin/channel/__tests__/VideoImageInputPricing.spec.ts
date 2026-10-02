import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import VideoImageInputPricing from '../VideoImageInputPricing.vue'
import type { VideoImageInputPricingForm } from '../videoPricing'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('Video reference image pricing form', () => {
  // 开关默认关闭；开启时单价保持未填，两个收费模式共享固定价格。
  it('supports disabling, first-image pricing, and a free allowance without changing the unit price', async () => {
    const wrapper = mount(VideoImageInputPricing, { props: { modelValue: null }, global: { stubs: { Select: true } } })
    const latest = () => wrapper.emitted('update:modelValue')?.at(-1)?.[0] as VideoImageInputPricingForm | null
    expect(wrapper.get('[role="switch"]').attributes('aria-checked')).toBe('false')
    expect(wrapper.find('input').exists()).toBe(false)
    await wrapper.get('[role="switch"]').trigger('click')
    expect(latest()).toEqual({ free_images: 0, price: null })
    await wrapper.setProps({ modelValue: latest() })
    expect(wrapper.find('[data-testid="video-image-free-count"]').exists()).toBe(false)
    await wrapper.get('[data-testid="video-image-unit-price"]').setValue('0.05')
    expect(latest()).toEqual({ free_images: 0, price: '0.05' })
    await wrapper.setProps({ modelValue: latest() })
    wrapper.getComponent({ name: 'Select' }).vm.$emit('update:modelValue', 'after_free')
    await wrapper.vm.$nextTick()
    expect(latest()).toEqual({ free_images: 1, price: '0.05' })
    await wrapper.setProps({ modelValue: latest() })
    await wrapper.get('[data-testid="video-image-free-count"]').setValue('3')
    expect(latest()).toEqual({ free_images: '3', price: '0.05' })
    await wrapper.setProps({ modelValue: latest() })
    wrapper.getComponent({ name: 'Select' }).vm.$emit('update:modelValue', 'from_first')
    await wrapper.vm.$nextTick()
    expect(latest()).toEqual({ free_images: 0, price: '0.05' })
    await wrapper.get('[role="switch"]').trigger('click')
    expect(latest()).toBeNull()
    wrapper.unmount()
  })
})
