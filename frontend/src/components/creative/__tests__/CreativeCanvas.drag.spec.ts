import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import CreativeCanvas from '@/components/creative/CreativeCanvas.vue'
import {
  CREATIVE_OUTPUT_DRAG_MIME,
  serializeCreativeOutputDrag,
} from '@/utils/creativeDrag'
import { outputAssetKey, type LocalAsset } from '@/utils/creativeLocalStore'

const fabricState = vi.hoisted(() => ({
  main: null as any,
  mask: null as any,
  images: [] as any[],
  scenePoint: { x: 200, y: 150 },
  viewportPoint: { x: 200, y: 150 },
}))

const storeMocks = vi.hoisted(() => ({
  saveAsset: vi.fn(),
  loadAsset: vi.fn(),
  loadSceneJson: vi.fn(),
  saveSceneJson: vi.fn(),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key,
  }),
}))

vi.mock('@/utils/creativeLocalStore', () => ({
  LocalStoreQuotaError: class LocalStoreQuotaError extends Error {},
  loadAsset: storeMocks.loadAsset,
  loadSceneJson: storeMocks.loadSceneJson,
  saveAsset: storeMocks.saveAsset,
  saveSceneJson: storeMocks.saveSceneJson,
  localAssetKey: (kind: string, id: string) => `${kind}:local:${id}`,
  outputAssetKey: (runId: string, outputIndex: number) => `output:${runId}:${outputIndex}`,
}))

vi.mock('fabric', () => {
  class MockObject {
    left = 0
    top = 0
    width = 0
    height = 0
    scaleX = 1
    scaleY = 1
    angle = 0
    data?: Record<string, unknown>
    selectable = true
    evented = true

    constructor(values?: Record<string, unknown>) {
      if (values) Object.assign(this, values)
    }

    set(values: Record<string, unknown>): this {
      Object.assign(this, values)
      return this
    }

    setCoords(): void {}

    getBoundingRect(): { left: number; top: number; width: number; height: number } {
      return {
        left: this.left,
        top: this.top,
        width: this.width * this.scaleX,
        height: this.height * this.scaleY,
      }
    }
  }

  class MockImage extends MockObject {
    width = 400
    height = 200

    getElement(): Partial<HTMLImageElement> {
      return { naturalWidth: 400, naturalHeight: 200, width: 400, height: 200 }
    }

    static async fromURL(): Promise<MockImage> {
      const image = new MockImage()
      fabricState.images.push(image)
      return image
    }
  }

  class MockCanvas {
    viewportTransform: [number, number, number, number, number, number] = [1, 0, 0, 1, 0, 0]
    width = 800
    height = 600
    backgroundColor = ''
    objects: MockObject[] = []
    active: MockObject | null = null
    listeners: Record<string, Array<(event: any) => void>> = {}

    constructor() {
      if (!fabricState.main) fabricState.main = this
      else fabricState.mask = this
    }

    on(name: string, handler: (event: any) => void): void {
      this.listeners[name] = [...(this.listeners[name] ?? []), handler]
    }
    emit(name: string, event: any): void {
      this.listeners[name]?.forEach((handler) => handler(event))
    }
    getWidth(): number { return this.width }
    getHeight(): number { return this.height }
    getZoom(): number { return this.viewportTransform[0] }
    getScenePoint = vi.fn(() => ({
      x: (fabricState.viewportPoint.x - this.viewportTransform[4]) / this.getZoom(),
      y: (fabricState.viewportPoint.y - this.viewportTransform[5]) / this.getZoom(),
    }))
    setViewportTransform(value: [number, number, number, number, number, number]): void {
      this.viewportTransform = value
    }
    zoomToPoint = vi.fn((_point: MockPoint, zoom: number): void => {
      this.viewportTransform[0] = zoom
      this.viewportTransform[3] = zoom
    })
    setDimensions(value: { width: number; height: number }): void {
      this.width = value.width
      this.height = value.height
    }
    requestRenderAll(): void {}
    add(...objects: MockObject[]): void { this.objects.push(...objects) }
    remove(...objects: MockObject[]): void {
      this.objects = this.objects.filter((object) => !objects.includes(object))
    }
    setActiveObject(object: MockObject): void { this.active = object }
    getActiveObject(): MockObject | null { return this.active }
    getActiveObjects(): MockObject[] { return this.active ? [this.active] : [] }
    discardActiveObject(): void { this.active = null }
    getObjects(): MockObject[] { return this.objects }
    toObject(): { objects: never[] } { return { objects: [] } }
    clear(): void { this.objects = [] }
    dispose(): void {}
  }

  class MockText extends MockObject {
    text: string
    width = 40
    height = 12

    constructor(text: string) {
      super()
      this.text = text
    }
  }

  class MockBrush {
    color = ''
    width = 0
    constructor() {}
  }

  class MockPoint {
    constructor(public x: number, public y: number) {}
  }

  return {
    Canvas: MockCanvas,
    FabricImage: MockImage,
    PencilBrush: MockBrush,
    Point: MockPoint,
    Rect: MockObject,
    StaticCanvas: MockCanvas,
    Text: MockText,
  }
})

function makeDropEvent(dataTransfer: Record<string, unknown>): DragEvent {
  const event = new Event('drop', { bubbles: true, cancelable: true }) as DragEvent
  Object.defineProperty(event, 'dataTransfer', { value: dataTransfer })
  return event
}

async function drop(wrapper: ReturnType<typeof mount>, dataTransfer: Record<string, unknown>): Promise<void> {
  wrapper.find('.dot-grid').element.dispatchEvent(makeDropEvent(dataTransfer))
  await flushPromises()
}

function makeWheelEvent(overrides: Partial<{
  deltaX: number
  deltaY: number
  deltaMode: number
  ctrlKey: boolean
  offsetX: number
  offsetY: number
}> = {}): WheelEvent {
  const event = new Event('wheel', { bubbles: true, cancelable: true }) as WheelEvent
  Object.defineProperties(event, {
    deltaX: { configurable: true, value: overrides.deltaX ?? 0 },
    deltaY: { configurable: true, value: overrides.deltaY ?? 0 },
    deltaMode: { configurable: true, value: overrides.deltaMode ?? 0 },
    ctrlKey: { configurable: true, value: overrides.ctrlKey ?? false },
    offsetX: { configurable: true, value: overrides.offsetX ?? 120 },
    offsetY: { configurable: true, value: overrides.offsetY ?? 80 },
  })
  return event
}

function emitWheel(overrides: Parameters<typeof makeWheelEvent>[0] = {}): WheelEvent {
  const event = makeWheelEvent(overrides)
  fabricState.main.emit('mouse:wheel', { e: event })
  return event
}

function makeTouchEvent(
  type: 'touchstart' | 'touchmove' | 'touchend' | 'touchcancel',
  touches: Array<{ clientX: number; clientY: number }>,
): TouchEvent {
  const event = new Event(type, { bubbles: true, cancelable: true }) as TouchEvent
  Object.defineProperties(event, {
    touches: { configurable: true, value: touches },
    changedTouches: { configurable: true, value: touches },
  })
  return event
}

function mountCanvas() {
  return mount(CreativeCanvas, {
    props: { operation: 'generate' },
    global: { stubs: { CropperModal: true, Icon: true } },
  })
}

describe('CreativeCanvas 拖放', () => {
  beforeEach(() => {
    fabricState.main = null
    fabricState.mask = null
    fabricState.images = []
    fabricState.scenePoint = { x: 200, y: 150 }
    fabricState.viewportPoint = { x: 200, y: 150 }
    storeMocks.saveAsset.mockReset().mockResolvedValue(undefined)
    storeMocks.loadAsset.mockReset().mockResolvedValue(null)
    storeMocks.loadSceneJson.mockReset().mockResolvedValue(null)
    storeMocks.saveSceneJson.mockReset().mockResolvedValue(undefined)
    Object.defineProperty(URL, 'createObjectURL', {
      configurable: true,
      value: vi.fn(() => 'blob:creative-test'),
    })
    Object.defineProperty(URL, 'revokeObjectURL', {
      configurable: true,
      value: vi.fn(),
    })
    vi.stubGlobal('createImageBitmap', vi.fn(async () => ({
      width: 400,
      height: 200,
      close: vi.fn(),
    })))
  })

  it('将外部图片以落点为中心放到画布并保存为源素材', async () => {
    const wrapper = mountCanvas()
    expect(fabricState.main).not.toBeNull()
    expect(wrapper.find('.dot-grid').exists()).toBe(true)
    const file = new File(['image'], 'source.png', { type: 'image/png' })

    await drop(wrapper, {
      types: ['Files'],
      files: [file],
      getData: vi.fn(() => ''),
      dropEffect: 'copy',
    })

    expect(storeMocks.saveAsset).toHaveBeenCalledWith(expect.objectContaining({ kind: 'source', blob: file }))
    expect(fabricState.images).toHaveLength(1)
    expect(fabricState.images[0]).toMatchObject({
      left: 150,
      top: 125,
      originX: 'left',
      originY: 'top',
      scaleX: 0.25,
      scaleY: 0.25,
    })
    wrapper.unmount()
  })

  it('多图拖入时从落点按固定间距斜向错开，单个保存失败不阻塞后续文件', async () => {
    storeMocks.saveAsset.mockRejectedValueOnce(new Error('quota')).mockResolvedValue(undefined)
    const wrapper = mountCanvas()
    const first = new File(['1'], 'first.png', { type: 'image/png' })
    const second = new File(['2'], 'second.webp', { type: 'image/webp' })

    await drop(wrapper, {
      types: ['Files'],
      files: [first, second],
      getData: vi.fn(() => ''),
      dropEffect: 'copy',
    })

    expect(fabricState.images).toHaveLength(1)
    expect(fabricState.images[0]).toMatchObject({ left: 190, top: 165 })
    wrapper.unmount()
  })

  it('按当前场景坐标把历史 output 拖到落点并保留运行记录元数据', async () => {
    const runId = 'crun_0123456789abcdef'
    const asset: LocalAsset = {
      key: outputAssetKey(runId, 1),
      kind: 'output',
      blob: new Blob(['output'], { type: 'image/png' }),
      runId,
      outputIndex: 1,
      createdAt: Date.now(),
    }
    storeMocks.loadAsset.mockResolvedValue(asset)
    const wrapper = mountCanvas()
    fabricState.main.viewportTransform = [2, 0, 0, 2, 20, 30]
    fabricState.viewportPoint = { x: 620, y: 530 }

    await drop(wrapper, {
      types: [CREATIVE_OUTPUT_DRAG_MIME],
      files: [],
      getData: vi.fn((type: string) => type === CREATIVE_OUTPUT_DRAG_MIME
        ? serializeCreativeOutputDrag({ runId, outputIndex: 1 })
        : ''),
      dropEffect: 'copy',
    })

    expect(storeMocks.loadAsset).toHaveBeenCalledWith(asset.key)
    expect(fabricState.main.getScenePoint).toHaveBeenCalled()
    expect(fabricState.images[0]).toMatchObject({
      left: 250,
      top: 225,
      scaleX: 0.25,
      data: expect.objectContaining({ runId, outputIndex: 1, assetKey: asset.key }),
    })
    wrapper.unmount()
  })

  it('不支持的文件类型不会创建画布对象并返回错误', async () => {
    const wrapper = mountCanvas()
    const file = new File(['gif'], 'image.gif', { type: 'image/gif' })

    await drop(wrapper, {
      types: ['Files'],
      files: [file],
      getData: vi.fn(() => ''),
      dropEffect: 'copy',
    })

    expect(storeMocks.saveAsset).not.toHaveBeenCalled()
    expect(fabricState.images).toHaveLength(0)
    expect(wrapper.emitted('error')).toEqual([['creative.error.dropUnsupported']])
    wrapper.unmount()
  })

  it('普通 wheel 只按跟手方向平移画布，不触发缩放', () => {
    const wrapper = mountCanvas()
    const event = emitWheel({ deltaX: 12, deltaY: -8 })

    expect(fabricState.main.viewportTransform).toEqual([1, 0, 0, 1, -12, 8])
    expect(fabricState.main.zoomToPoint).not.toHaveBeenCalled()
    expect(event.defaultPrevented).toBe(true)
    expect(event.cancelBubble).toBe(true)
    wrapper.unmount()
  })

  it('按 wheel 单位将行和页换算为画布像素', () => {
    const wrapper = mountCanvas()
    emitWheel({ deltaX: 2, deltaY: 3, deltaMode: 1 })
    expect(fabricState.main.viewportTransform).toEqual([1, 0, 0, 1, -32, -48])

    fabricState.main.setViewportTransform([1, 0, 0, 1, 0, 0])
    emitWheel({ deltaX: 1, deltaY: 1, deltaMode: 2 })
    expect(fabricState.main.viewportTransform).toEqual([1, 0, 0, 1, -800, -600])
    wrapper.unmount()
  })

  it('ctrlKey wheel 以光标为中心缩放并使用加快后的灵敏度', () => {
    const wrapper = mountCanvas()
    const event = emitWheel({ deltaY: 10, ctrlKey: true, offsetX: 230, offsetY: 170 })
    const [point, zoom] = fabricState.main.zoomToPoint.mock.calls[0]

    expect(point).toMatchObject({ x: 230, y: 170 })
    expect(zoom).toBeCloseTo(0.995 ** 10, 10)
    expect(fabricState.main.viewportTransform.slice(4)).toEqual([0, 0])
    expect(event.defaultPrevented).toBe(true)
    wrapper.unmount()
  })

  it('捏合缩放保持 0.2 到 3 的边界', () => {
    const wrapper = mountCanvas()
    fabricState.main.setViewportTransform([3, 0, 0, 3, 0, 0])
    emitWheel({ deltaY: -100, ctrlKey: true })
    expect(fabricState.main.getZoom()).toBe(3)

    fabricState.main.setViewportTransform([0.2, 0, 0, 0.2, 0, 0])
    emitWheel({ deltaY: 100, ctrlKey: true })
    expect(fabricState.main.getZoom()).toBe(0.2)
    wrapper.unmount()
  })

  it('移动端双指缩放以手势中点下的场景点为锚并同步平移', () => {
    const wrapper = mountCanvas()
    fabricState.viewportPoint = { x: 150, y: 100 }
    const container = wrapper.find('.dot-grid').element
    container.dispatchEvent(makeTouchEvent('touchstart', [
      { clientX: 100, clientY: 100 },
      { clientX: 200, clientY: 100 },
    ]))
    const move = makeTouchEvent('touchmove', [
      { clientX: 100, clientY: 100 },
      { clientX: 220, clientY: 100 },
    ])
    container.dispatchEvent(move)

    expect(fabricState.main.getZoom()).toBeCloseTo(1.2)
    expect(fabricState.main.viewportTransform.slice(4)).toEqual([-20, -20])
    expect(move.defaultPrevented).toBe(true)
    wrapper.unmount()
  })

  it('局部重绘选中图片时，锚点描边与图片左上角对齐', async () => {
    const wrapper = mount(CreativeCanvas, {
      props: { operation: 'inpaint' },
      global: { stubs: { CropperModal: true, Icon: true } },
    })
    const source = new File(['image'], 'source.png', { type: 'image/png' })
    await (wrapper.vm as unknown as { addUploadedImage: (blob: Blob) => Promise<void> }).addUploadedImage(source)
    const image = fabricState.images[0]
    fabricState.main.emit('selection:created', { selected: [image] })
    await flushPromises()

    const outline = fabricState.main.objects.find((object: { data?: Record<string, unknown> }) => object.data?.kind === 'anchor-outline')
    expect(outline).toMatchObject({
      left: image.left,
      top: image.top,
      width: image.width * image.scaleX,
      height: image.height * image.scaleY,
      originX: 'left',
      originY: 'top',
    })
    wrapper.unmount()
  })
})
