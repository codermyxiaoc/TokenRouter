import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import VideoTokenPrepay from '../VideoTokenPrepay.vue'
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('Video Token prepayment', () => {
  // 开关不隐式赠送零价，明确填写的零价可保存，关闭彻底清空配置。
  it('requires an explicit price on enable and emits null on disable', async () => {
    const wrapper = mount(VideoTokenPrepay)
    expect(wrapper.get('[role="switch"]').attributes('aria-checked')).toBe('false')
    expect(wrapper.find('input').exists()).toBe(false)
    await wrapper.get('[role="switch"]').trigger('click')
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toEqual({ price_per_second: null })
    await wrapper.setProps({ modelValue: { price_per_second: null } })
    await wrapper.get('input').setValue('0')
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toEqual({ price_per_second: '0' })
    await wrapper.get('[role="switch"]').trigger('click')
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toBeNull()
  })
})
