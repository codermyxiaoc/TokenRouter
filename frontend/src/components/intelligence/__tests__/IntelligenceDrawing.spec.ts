import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import IntelligenceDrawing from '../IntelligenceDrawing.vue'
import { intelligenceResultsKey } from '../useIntelligenceResults'

vi.mock('vue-i18n', async (importOriginal) => ({ ...(await importOriginal<typeof import('vue-i18n')>()), useI18n: () => ({ t: (key: string) => key }) }))
const preview = vi.fn()
let visibleCallback: IntersectionObserverCallback

describe('画图预览隔离', () => {
  beforeEach(() => {
    preview.mockReset().mockResolvedValue({ url: '/api/v1/intelligence-tests/preview-content/a.b', expires_at: new Date(Date.now() + 300000).toISOString() })
    vi.stubGlobal('IntersectionObserver', class { constructor(callback: IntersectionObserverCallback) { visibleCallback = callback } observe() {} disconnect() {} })
  })
  it('可见后才申请票据，以无同源能力sandbox加载；离屏销毁iframe', async () => {
    const wrapper = mount(IntelligenceDrawing, { props: { runId: 'run-1', title: 'drawing' }, global: { provide: { [intelligenceResultsKey as symbol]: { preview } } } })
    expect(preview).not.toHaveBeenCalled()
    visibleCallback([{ isIntersecting: true } as IntersectionObserverEntry], {} as IntersectionObserver)
    await flushPromises()
    expect(preview).toHaveBeenCalledWith('run-1')
    const frame = wrapper.get('iframe')
    expect(frame.attributes('sandbox')).toBe('allow-scripts')
    expect(frame.attributes('referrerpolicy')).toBe('no-referrer')
    expect(frame.attributes('srcdoc')).toBeUndefined()
    expect(frame.attributes('src')).toBe('/api/v1/intelligence-tests/preview-content/a.b')
    visibleCallback([{ isIntersecting: false } as IntersectionObserverEntry], {} as IntersectionObserver)
    await flushPromises()
    expect(wrapper.find('iframe').exists()).toBe(false)
    wrapper.unmount()
  })
  it('错误URL不会交给iframe执行', async () => {
    preview.mockResolvedValue({ url: 'https://evil.example/run', expires_at: '' })
    const wrapper = mount(IntelligenceDrawing, { props: { runId: 'run-1', title: 'drawing' }, global: { provide: { [intelligenceResultsKey as symbol]: { preview } } } })
    visibleCallback([{ isIntersecting: true } as IntersectionObserverEntry], {} as IntersectionObserver)
    await flushPromises()
    expect(wrapper.find('iframe').exists()).toBe(false)
    expect(wrapper.text()).toContain('intelligence.noArtwork')
    wrapper.unmount()
  })
})
