/**
 * creativeLocalStore（IndexedDB）测试
 * 使用 fake-indexeddb 提供内存版 IndexedDB；配额错误通过 stub put 注入。
 */
import 'fake-indexeddb/auto'
import { Blob as NodeBlob } from 'node:buffer'
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'

// jsdom 的 Blob 无法被 Node structuredClone 正确克隆（fake-indexeddb 写入时依赖它），
// 换成 Node 原生 Blob 保证 IndexedDB 结构化克隆往返一致
globalThis.Blob = NodeBlob as unknown as typeof Blob
import {
	CREATIVE_WORKSPACE_STORAGE_KEY,
	LocalStoreQuotaError,
	LocalStoreError,
	__resetCreativeStoreForTest,
  clearAll,
  clearKind,
	deleteAsset,
	getCreativeWorkspaceId,
	listAssets,
  loadAsset,
  loadSceneJson,
  loadSetting,
  localAssetKey,
	outputAssetKey,
	rotateCreativeWorkspaceId,
	saveAsset,
  saveSceneJson,
  saveSetting,
  type LocalAsset,
} from '@/utils/creativeLocalStore'

function makeAsset(partial: Partial<LocalAsset> & Pick<LocalAsset, 'key' | 'kind'>): LocalAsset {
  return {
    blob: new Blob(['x'], { type: 'image/png' }),
    createdAt: Date.now(),
    ...partial,
  }
}

describe('creativeLocalStore', () => {
  beforeEach(async () => {
    __resetCreativeStoreForTest()
    localStorage.clear()
    await clearAll()
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('saveAsset/loadAsset 往返保存 blob 与元信息', async () => {
    const asset = makeAsset({
      key: outputAssetKey('run-1', 2),
      kind: 'output',
      runId: 'run-1',
      outputIndex: 2,
    })

    await saveAsset(asset)
    const loaded = await loadAsset(asset.key)

    expect(loaded).not.toBeNull()
    expect(loaded?.kind).toBe('output')
    expect(loaded?.runId).toBe('run-1')
    expect(loaded?.outputIndex).toBe(2)
    expect(loaded?.blob.size).toBe(asset.blob.size)
    expect(loaded?.createdAt).toBe(asset.createdAt)
  })

  it('loadAsset 不存在时返回 null', async () => {
    expect(await loadAsset('missing:key')).toBeNull()
  })

  it('listAssets 按 kind 过滤并按 createdAt 升序', async () => {
    const sourceA = makeAsset({ key: localAssetKey('source', 'a'), kind: 'source', createdAt: 200 })
    const sourceB = makeAsset({ key: localAssetKey('source', 'b'), kind: 'source', createdAt: 100 })
    const output = makeAsset({ key: outputAssetKey('run-1', 0), kind: 'output', createdAt: 50 })

    await saveAsset(sourceA)
    await saveAsset(output)
    await saveAsset(sourceB)

    const sources = await listAssets('source')
    expect(sources.map((a) => a.key)).toEqual([sourceB.key, sourceA.key])
    expect((await listAssets('output')).map((a) => a.key)).toEqual([output.key])
    expect(await listAssets('mask')).toEqual([])
  })

  it('deleteAsset 幂等：重复删除不报错，删除后读取为 null', async () => {
    const asset = makeAsset({ key: localAssetKey('source', 'del'), kind: 'source' })
    await saveAsset(asset)

    await deleteAsset(asset.key)
    await deleteAsset(asset.key)
    expect(await loadAsset(asset.key)).toBeNull()
  })

  it('saveSceneJson/loadSceneJson 往返', async () => {
    expect(await loadSceneJson('creative:canvas')).toBeNull()
    await saveSceneJson('creative:canvas', '{"objects":[]}')
    expect(await loadSceneJson('creative:canvas')).toBe('{"objects":[]}')
  })

	it('saveSetting/loadSetting 往返且覆盖写', async () => {
		expect(await loadSetting('creative:selection')).toBeNull()
		await saveSetting('creative:selection', { optionKey: 'a' })
		expect(await loadSetting('creative:selection')).toEqual({ optionKey: 'a' })
		await saveSetting('creative:selection', { optionKey: 'b' })
		expect(await loadSetting('creative:selection')).toEqual({ optionKey: 'b' })
	})

	it('工作区首次生成并在同源标签页之间复用', () => {
		const first = getCreativeWorkspaceId()

		expect(first).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
		expect(localStorage.getItem(CREATIVE_WORKSPACE_STORAGE_KEY)).toBe(first)
		expect(getCreativeWorkspaceId()).toBe(first)
	})

	it('非法工作区值会重新生成，旋转后与原值不同', () => {
		localStorage.setItem(CREATIVE_WORKSPACE_STORAGE_KEY, 'invalid')
		const generated = getCreativeWorkspaceId()
		expect(generated).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)
		expect(generated).not.toBe('invalid')

		const rotated = rotateCreativeWorkspaceId()
		expect(rotated).not.toBe(generated)
		expect(localStorage.getItem(CREATIVE_WORKSPACE_STORAGE_KEY)).toBe(rotated)
	})

	it('本地存储不可用时工作区读取失败，不回退到共享标识', () => {
		vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
			throw new DOMException('blocked', 'SecurityError')
		})

		expect(() => getCreativeWorkspaceId()).toThrow(LocalStoreError)
	})

  it('clearAll 清空素材、场景与设置', async () => {
    await saveAsset(makeAsset({ key: localAssetKey('source', 'c'), kind: 'source' }))
    await saveSceneJson('creative:canvas', '{}')
    await saveSetting('creative:selection', {})

    await clearAll()

    expect(await listAssets('source')).toEqual([])
    expect(await loadSceneJson('creative:canvas')).toBeNull()
    expect(await loadSetting('creative:selection')).toBeNull()
  })

  it('clearKind 只清空指定种类的素材', async () => {
    const source = makeAsset({ key: localAssetKey('source', 'k1'), kind: 'source' })
    const output = makeAsset({ key: outputAssetKey('run-1', 0), kind: 'output' })
    await saveAsset(source)
    await saveAsset(output)

    await clearKind('source')

    expect(await listAssets('source')).toEqual([])
    expect((await listAssets('output')).map((a) => a.key)).toEqual([output.key])
  })

  it('clearAll 底层失败归一为 LocalStoreError', async () => {
    const failure = new Error('idb down')
    vi.spyOn(IDBObjectStore.prototype, 'clear').mockImplementation(() => {
      const request: Record<string, unknown> = { error: null, result: undefined }
      queueMicrotask(() => {
        request.error = failure
        const onerror = request.onerror as ((event: unknown) => void) | null
        onerror?.call(request, { target: request })
      })
      return request as unknown as IDBRequest
    })

    await expect(clearAll()).rejects.toMatchObject({ name: 'LocalStoreError' })
  })

  it('QuotaExceededError 归一为带 type 标志的 LocalStoreQuotaError', async () => {
    const quotaError = new DOMException('quota exceeded', 'QuotaExceededError')
    // stub 底层 put：返回一个异步触发 onerror 的假 request
    vi.spyOn(IDBObjectStore.prototype, 'put').mockImplementation(() => {
      const request: Record<string, unknown> = { error: null, result: undefined }
      queueMicrotask(() => {
        request.error = quotaError
        const onerror = request.onerror as ((event: unknown) => void) | null
        onerror?.call(request, { target: request })
      })
      return request as unknown as IDBRequest
    })

    const attempt = saveAsset(makeAsset({ key: localAssetKey('source', 'quota'), kind: 'source' }))
    await expect(attempt).rejects.toBeInstanceOf(LocalStoreQuotaError)
    await expect(attempt).rejects.toMatchObject({ type: 'quota' })
  })
})
