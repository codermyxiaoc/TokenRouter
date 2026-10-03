import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount, type VueWrapper } from '@vue/test-utils'
import IntelligenceDrawingGallery from '../IntelligenceDrawingGallery.vue'
import { runFixture } from './fixtures'

vi.mock('vue-i18n', async (importOriginal) => ({ ...(await importOriginal<typeof import('vue-i18n')>()), useI18n: () => ({ t: (key: string) => key }) }))

const wrappers: VueWrapper[] = []
const artwork = runFixture({ id: 'drawing-1', benchmark: 'drawing', has_artifact: true })

function mountGallery() {
  const wrapper = mount(IntelligenceDrawingGallery, {
    attachTo: document.body,
    props: { artifacts: [artwork], model: 'model-a' },
    global: { stubs: { IntelligenceDrawing: { props: ['thumbnail'], template: '<div data-testid="drawing" :data-thumbnail="thumbnail" />' } } },
  })
  wrappers.push(wrapper)
  const element = wrapper.get('[data-testid="drawing-gallery"]').element as HTMLElement
  // jsdom 没有布局和指针捕获，显式模拟画廊溢出及浏览器捕获能力。
  Object.defineProperties(element, {
    clientWidth: { configurable: true, value: 360 },
    scrollWidth: { configurable: true, value: 1440 },
  })
  const captured = new Set<number>()
  element.setPointerCapture = vi.fn((id: number) => { captured.add(id) })
  element.hasPointerCapture = vi.fn((id: number) => captured.has(id))
  element.releasePointerCapture = vi.fn((id: number) => { captured.delete(id) })
  return { wrapper, element }
}

function pointer(target: Element, type: string, overrides: Record<string, unknown> = {}) {
  const event = new MouseEvent(type, { bubbles: true, cancelable: true, button: 0, buttons: type === 'pointerup' ? 0 : 1, clientX: 200 })
  for (const [key, value] of Object.entries({ pointerId: 1, pointerType: 'mouse', ...overrides })) {
    Object.defineProperty(event, key, { value })
  }
  target.dispatchEvent(event)
  return event
}

function wheel(target: Element, options: WheelEventInit) {
  const event = new WheelEvent('wheel', { bubbles: true, cancelable: true, ...options })
  target.dispatchEvent(event)
  return event
}

afterEach(() => { for (const wrapper of wrappers.splice(0)) wrapper.unmount() })

describe('画图作品横向浏览', () => {
  it('普通滚轮可横移；到边界或无溢出时仍允许页面滚动', () => {
    const { element } = mountGallery()
    expect(wheel(element, { deltaY: 120 }).defaultPrevented).toBe(true)
    expect(element.scrollLeft).toBe(120)
    element.scrollLeft = 1080
    expect(wheel(element, { deltaY: 120 }).defaultPrevented).toBe(false)
    expect(element.scrollLeft).toBe(1080)
    element.scrollLeft = 0
    expect(wheel(element, { deltaY: -120 }).defaultPrevented).toBe(false)
    Object.defineProperty(element, 'scrollWidth', { configurable: true, value: 360 })
    expect(wheel(element, { deltaY: 120 }).defaultPrevented).toBe(false)
  })

  it('保留横向触控板方向及 Ctrl 滚轮缩放，支持行和页单位', () => {
    const { element } = mountGallery()
    expect(wheel(element, { deltaX: 80, deltaY: 10 }).defaultPrevented).toBe(true)
    expect(element.scrollLeft).toBe(80)
    expect(wheel(element, { deltaY: 120, ctrlKey: true }).defaultPrevented).toBe(false)
    expect(element.scrollLeft).toBe(80)
    wheel(element, { deltaY: 2, deltaMode: 1 })
    expect(element.scrollLeft).toBeGreaterThan(82)
    const beforePage = element.scrollLeft
    wheel(element, { deltaY: 1, deltaMode: 2 })
    expect(element.scrollLeft - beforePage).toBe(360)
  })

  it('鼠标超过阈值后拖动，不跟随其他指针且松开后停止', () => {
    const { element } = mountGallery()
    pointer(element, 'pointerdown')
    pointer(element, 'pointermove', { clientX: 197 })
    expect(element.scrollLeft).toBe(0)
    pointer(element, 'pointermove', { pointerId: 2, clientX: 100 })
    expect(element.scrollLeft).toBe(0)
    pointer(element, 'pointerup', { pointerId: 2 })
    pointer(element, 'pointermove', { clientX: 100 })
    expect(element.scrollLeft).toBe(100)
    pointer(element, 'pointerup', { clientX: 100 })
    pointer(element, 'pointermove', { clientX: 50 })
    expect(element.scrollLeft).toBe(100)
  })

  it('触屏交给原生滚动，右键和作品按钮起点不启动拖动', () => {
    const { wrapper, element } = mountGallery()
    const button = wrapper.get('button').element
    for (const options of [{ pointerType: 'touch' }, { button: 2 }]) {
      const down = pointer(element, 'pointerdown', options)
      pointer(element, 'pointermove', { ...options, clientX: 100 })
      pointer(element, 'pointerup', options)
      expect(down.defaultPrevented).toBe(false)
      expect(element.scrollLeft).toBe(0)
    }
    pointer(button, 'pointerdown')
    pointer(element, 'pointermove', { clientX: 100 })
    pointer(element, 'pointerup')
    expect(element.scrollLeft).toBe(0)
    button.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, detail: 1 }))
    expect(wrapper.emitted('select')).toEqual([[artwork]])
  })

  it('拖动松手不误打开作品，之后正常点击仍可打开', () => {
    const { wrapper, element } = mountGallery()
    const button = wrapper.get('button').element
    pointer(element, 'pointerdown')
    pointer(element, 'pointermove', { clientX: 100 })
    expect(element.setPointerCapture).toHaveBeenCalledWith(1)
    pointer(element, 'pointerup')
    // 浏览器会把捕获期间的点击指向画廊，而不是松手位置下方的作品按钮。
    element.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, detail: 1 }))
    expect(wrapper.emitted('select')).toBeUndefined()
    pointer(button, 'pointerdown')
    pointer(button, 'pointerup')
    button.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, detail: 1 }))
    expect(wrapper.emitted('select')).toEqual([[artwork]])
  })

  it('丢失松手事件或窗口失焦后不继续拖动', () => {
    const { element } = mountGallery()
    pointer(element, 'pointerdown')
    pointer(element, 'pointermove', { clientX: 100 })
    pointer(element, 'pointermove', { clientX: 50, buttons: 0 })
    expect(element.scrollLeft).toBe(100)
    pointer(element, 'pointermove', { clientX: 25 })
    expect(element.scrollLeft).toBe(100)
    pointer(element, 'pointerdown')
    window.dispatchEvent(new Event('blur'))
    pointer(element, 'pointermove', { clientX: 100 })
    expect(element.scrollLeft).toBe(100)
  })

  it('取消指针后结束拖动，各画廊滚动互不影响', () => {
    const first = mountGallery()
    const second = mountGallery()
    pointer(first.element, 'pointerdown')
    pointer(first.element, 'pointermove', { clientX: 100 })
    expect(second.element.scrollLeft).toBe(0)
    pointer(first.element, 'pointercancel')
    pointer(first.element, 'pointermove', { clientX: 50 })
    expect(first.element.scrollLeft).toBe(100)
    wheel(second.element, { deltaY: 60 })
    expect(second.element.scrollLeft).toBe(60)
    expect(first.element.scrollLeft).toBe(100)
  })

  it('键盘可浏览画廊，按钮上的方向键不被画廊劫持', () => {
    const { wrapper, element } = mountGallery()
    expect(element.getAttribute('tabindex')).toBe('0')
    expect(element.getAttribute('role')).toBe('region')
    const key = (target: Element, value: string) => {
      const event = new KeyboardEvent('keydown', { key: value, bubbles: true, cancelable: true })
      target.dispatchEvent(event)
      return event
    }
    expect(key(element, 'ArrowRight').defaultPrevented).toBe(true)
    expect(element.scrollLeft).toBeGreaterThan(0)
    key(element, 'End')
    expect(element.scrollLeft).toBe(1080)
    key(element, 'Home')
    expect(element.scrollLeft).toBe(0)
    key(element, 'PageDown')
    expect(element.scrollLeft).toBe(360)
    key(element, 'PageUp')
    expect(element.scrollLeft).toBe(0)
    expect(key(wrapper.get('button').element, 'ArrowRight').defaultPrevented).toBe(false)
    expect(element.scrollLeft).toBe(0)
  })
})
