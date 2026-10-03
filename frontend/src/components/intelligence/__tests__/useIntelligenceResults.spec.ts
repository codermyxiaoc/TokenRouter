import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises } from '@vue/test-utils'
import { createIntelligenceResultsLoader } from '../useIntelligenceResults'
import { runFixture } from './fixtures'

const { detail, preview } = vi.hoisted(() => ({ detail: vi.fn(), preview: vi.fn() }))
vi.mock('@/api/intelligence', () => ({ intelligenceAPI: { detail, preview } }))

describe('检测产物按需加载', () => {
  beforeEach(() => { detail.mockReset(); preview.mockReset() })

  it('同记录合并并发读取，终态详情缓存，管理员域独立', async () => {
    detail.mockResolvedValue(runFixture())
    const loader = createIntelligenceResultsLoader(true)
    await Promise.all([loader.detail('run-1'), loader.detail('run-1')])
    await loader.detail('run-1')
    expect(detail).toHaveBeenCalledTimes(1)
    expect(detail).toHaveBeenCalledWith('run-1', true, expect.any(AbortSignal))
    loader.dispose()
  })

  it('运行中详情不缓存，过期预览票据重新签发', async () => {
    detail.mockResolvedValue(runFixture({ status: 'running' }))
    preview.mockResolvedValueOnce({ url: 'first', expires_at: new Date(Date.now() + 1000).toISOString() }).mockResolvedValue({ url: 'next', expires_at: new Date(Date.now() + 300000).toISOString() })
    const loader = createIntelligenceResultsLoader()
    await loader.detail('run-1')
    await loader.detail('run-1')
    expect(detail).toHaveBeenCalledTimes(2)
    await loader.preview('run-1')
    expect((await loader.preview('run-1')).url).toBe('next')
    await loader.preview('run-1')
    expect(preview).toHaveBeenCalledTimes(2)
    loader.dispose()
  })

  it('多个可见卡片最多两个并发请求，完成后推进队列', async () => {
    const resolvers: Array<(value: unknown) => void> = []
    preview.mockImplementation(() => new Promise(resolve => resolvers.push(resolve)))
    const loader = createIntelligenceResultsLoader()
    const jobs = [loader.preview('a'), loader.preview('b'), loader.preview('c')]
    expect(preview).toHaveBeenCalledTimes(2)
    resolvers[0]({ url: 'a', expires_at: new Date(Date.now() + 300000).toISOString() })
    await flushPromises()
    expect(preview).toHaveBeenCalledTimes(3)
    resolvers[1]({ url: 'b', expires_at: new Date(Date.now() + 300000).toISOString() })
    resolvers[2]({ url: 'c', expires_at: new Date(Date.now() + 300000).toISOString() })
    await Promise.all(jobs)
    loader.dispose()
  })
})
