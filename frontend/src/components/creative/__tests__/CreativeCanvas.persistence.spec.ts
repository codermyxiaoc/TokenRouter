import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import CreativeCanvas from '@/components/creative/CreativeCanvas.vue'
import { outputAssetKey, type LocalAsset } from '@/utils/creativeLocalStore'

const fabricState = vi.hoisted(() => ({
  main: null as any,
  mask: null as any,
  images: [] as any[],
}))

const storeMocks = vi.hoisted(() => ({
  saveAsset: vi.fn(),
  loadAsset: vi.fn(),
  loadSceneJson: vi.fn(),
  saveSceneJson: vi.fn(),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
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
    loadedJSON: Record<string, unknown> | null = null

    constructor() {
      if (!fabricState.main) fabricState.main = this
      else fabricState.mask = this
    }

    on(): void {}
    getWidth(): number { return this.width }
    getHeight(): number { return this.height }
    getZoom(): number { return this.viewportTransform[0] }
    setViewportTransform(value: [number, number, number, number, number, number]): void {
      this.viewportTransform = value
    }
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
    toObject(): { objects: Array<Record<string, unknown>> } {
      return {
        objects: this.objects.map((object) => ({
          type: 'Image',
          left: object.left,
          top: object.top,
          scaleX: object.scaleX,
          scaleY: object.scaleY,
          data: object.data,
        })),
      }
    }
    async loadFromJSON(json: Record<string, unknown>): Promise<this> {
      this.loadedJSON = json
      this.objects = []
      return this
    }
    clear(): void { this.objects = [] }
    dispose(): void {}
  }

  class MockBrush {
    color = ''
    width = 0
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
    Text: class MockText extends MockObject {
      text: string
      constructor(text: string) {
        super()
        this.text = text
      }
    },
  }
})

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise
  })
  return { promise, resolve }
}

function mountCanvas() {
  return mount(CreativeCanvas, {
    props: { operation: 'generate' },
    global: { stubs: { CropperModal: true, Icon: true } },
  })
}

describe('CreativeCanvas 本地恢复与快照持久化', () => {
  beforeEach(() => {
    fabricState.main = null
    fabricState.mask = null
    fabricState.images = []
    storeMocks.saveAsset.mockReset().mockResolvedValue(undefined)
    storeMocks.loadAsset.mockReset().mockResolvedValue(null)
    storeMocks.loadSceneJson.mockReset().mockResolvedValue(null)
    storeMocks.saveSceneJson.mockReset().mockResolvedValue(undefined)
    Object.defineProperty(URL, 'createObjectURL', {
      configurable: true,
      value: vi.fn(() => 'blob:creative-persistence-test'),
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

  it('恢复未完成时卸载不会用空画布覆盖已有快照', async () => {
    const pending = deferred<string | null>()
    storeMocks.loadSceneJson.mockReturnValue(pending.promise)
    const wrapper = mountCanvas()

    wrapper.unmount()
    expect(storeMocks.saveSceneJson).not.toHaveBeenCalled()

    pending.resolve(JSON.stringify({ objects: [] }))
    await flushPromises()
    expect(storeMocks.saveSceneJson).not.toHaveBeenCalled()
  })

  it('恢复期间上板会等待 loadFromJSON 完成，最终对象不会被清掉', async () => {
    const pending = deferred<string | null>()
    storeMocks.loadSceneJson.mockReturnValue(pending.promise)
    const wrapper = mountCanvas()
    const output = {
      blob: new Blob(['output'], { type: 'image/png' }),
      runId: 'crun_0123456789abcdef',
      outputIndex: 0,
    }

    const placement = (wrapper.vm as any).placeOutput(output)
    expect(fabricState.images).toHaveLength(0)

    pending.resolve(JSON.stringify({ objects: [] }))
    await placement

    expect(fabricState.images).toHaveLength(1)
    wrapper.unmount()
  })

  it('从 IndexedDB 恢复图片占位符并保留画布变换', async () => {
    const assetKey = outputAssetKey('crun_0123456789abcdef', 0)
    const asset: LocalAsset = {
      key: assetKey,
      kind: 'output',
      blob: new Blob(['output'], { type: 'image/png' }),
      runId: 'crun_0123456789abcdef',
      outputIndex: 0,
      createdAt: 1,
    }
    storeMocks.loadSceneJson.mockResolvedValue(JSON.stringify({
      viewportTransform: [1.25, 0, 0, 1.25, 32, 48],
      objects: [{ type: 'image', src: `asset://${assetKey}`, data: { kind: 'image', assetKey } }],
    }))
    storeMocks.loadAsset.mockResolvedValue(asset)
    const wrapper = mountCanvas()
    await flushPromises()

    expect(fabricState.main.loadedJSON).toEqual(expect.objectContaining({
      viewportTransform: [1.25, 0, 0, 1.25, 32, 48],
    }))
    expect((fabricState.main.loadedJSON?.objects as Array<Record<string, unknown>>)[0].src).toBe(
      'blob:creative-persistence-test',
    )
    expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:creative-persistence-test')
    wrapper.unmount()
  })

  it('页面隐藏时刷新有变更的快照', async () => {
    const wrapper = mountCanvas()
    await flushPromises()

    await (wrapper.vm as any).addUploadedImage(new Blob(['source'], { type: 'image/png' }))
    window.dispatchEvent(new Event('pagehide'))
    await flushPromises()

    expect(storeMocks.saveSceneJson).toHaveBeenCalledWith('creative:canvas', expect.any(String))
    const savedJson = storeMocks.saveSceneJson.mock.calls[0]?.[1] as string
    expect(savedJson).toContain('asset://source:local:')
    wrapper.unmount()
  })

  it('本地素材缺失时只跳过图片，不保存被裁剪的空快照', async () => {
    const assetKey = outputAssetKey('crun_0123456789abcdef', 0)
    storeMocks.loadSceneJson.mockResolvedValue(JSON.stringify({
      objects: [{ type: 'image', src: `asset://${assetKey}`, data: { kind: 'image', assetKey } }],
    }))
    storeMocks.loadAsset.mockResolvedValue(null)
    const wrapper = mountCanvas()
    await flushPromises()

    expect(storeMocks.saveSceneJson).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
