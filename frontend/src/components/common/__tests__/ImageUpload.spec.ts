import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import ImageUpload from '../ImageUpload.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
afterEach(() => vi.unstubAllGlobals())

describe('图片上传读取隔离', () => {
  it('新选择或删除后取消旧读取，并忽略已排队的旧 onload', async () => {
    // 模拟文件读取乱序完成，验证 abort 之外仍有结果隔离。
    const readers: MockReader[] = []
    class MockReader {
      onload: ((event: { target: { result: string } }) => void) | null = null
      onerror: (() => void) | null = null
      abort = vi.fn()
      readAsDataURL = vi.fn()
      constructor() { readers.push(this) }
    }
    vi.stubGlobal('FileReader', MockReader)
    const wrapper = mount(ImageUpload, { props: { modelValue: 'initial', mode: 'image' } })
    const input = wrapper.get('input[type="file"]')
    Object.defineProperty(input.element, 'files', { configurable: true, value: [new File(['1'], 'one.png', { type: 'image/png' })] })
    await input.trigger('change')
    Object.defineProperty(input.element, 'files', { configurable: true, value: [new File(['2'], 'two.png', { type: 'image/png' })] })
    await input.trigger('change')
    expect(readers[0].abort).toHaveBeenCalled()
    readers[1].onload?.({ target: { result: 'second' } })
    readers[0].onload?.({ target: { result: 'first' } })
    expect(wrapper.emitted('update:modelValue')).toEqual([['second']])
    await wrapper.findAll('button').find(node => node.text().includes('common.remove'))!.trigger('click')
    readers[1].onload?.({ target: { result: 'late' } })
    expect(wrapper.emitted('update:modelValue')).toEqual([['second'], ['']])
    wrapper.unmount()
  })
})
