import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import IntervalRow from '../IntervalRow.vue'
import ModelTagInput from '../ModelTagInput.vue'
import SearchInput from '@/components/common/SearchInput.vue'
import type { IntervalFormEntry } from '../types'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
afterEach(() => vi.useRealTimers())

describe('配置输入兼容性', () => {
  it('科学计数法按完整 Token 数解析，空上限继续表示不限', async () => {
    const wrapper = mount(IntervalRow, {
      props: { mode: 'token', interval: { min_tokens: 0, max_tokens: null } as IntervalFormEntry },
      global: { stubs: { Icon: true } }
    })
    const inputs = wrapper.findAll('input')
    await inputs[0].setValue('1e6')
    await inputs[1].setValue('2e6')
    await inputs[1].setValue('')
    expect(wrapper.emitted('update')?.map(event => event[0])).toEqual([
      { min_tokens: 1000000, max_tokens: null },
      { min_tokens: 0, max_tokens: 2000000 },
      { min_tokens: 0, max_tokens: null }
    ])
    wrapper.unmount()
  })

  it('输入法确认模型名称时不提前提交或删除标签', async () => {
    const wrapper = mount(ModelTagInput, { props: { models: ['existing'] } })
    const input = wrapper.get('input')
    await input.setValue('候选')
    await input.trigger('keydown', { key: 'Enter', isComposing: true })
    await input.trigger('keydown', { key: 'Tab', isComposing: true })
    expect(wrapper.emitted('update:models')).toBeUndefined()
    await input.trigger('keydown', { key: 'Enter', isComposing: false })
    expect(wrapper.emitted('update:models')?.[0]).toEqual([['existing', '候选']])
    await input.trigger('keydown', { key: 'Backspace', isComposing: true })
    expect(wrapper.emitted('update:models')).toHaveLength(1)
    wrapper.unmount()
  })

  it('中文搜索仅在组合输入确认后触发', async () => {
    vi.useFakeTimers()
    const wrapper = mount(SearchInput, { props: { modelValue: '', debounceMs: 20 } })
    const input = wrapper.get('input')
    await input.trigger('compositionstart')
    input.element.value = 'zhong'
    await input.trigger('input')
    await vi.advanceTimersByTimeAsync(30)
    expect(wrapper.emitted('search')).toBeUndefined()
    input.element.value = '中文'
    await input.trigger('compositionend')
    await vi.advanceTimersByTimeAsync(30)
    expect(wrapper.emitted('search')).toEqual([['中文']])
    wrapper.unmount()
  })
})
