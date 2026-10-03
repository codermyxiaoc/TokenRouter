import { inject, onBeforeUnmount, provide, type InjectionKey } from 'vue'
import { intelligenceAPI, type IntelligencePreview, type IntelligenceRun } from '@/api/intelligence'

export interface IntelligenceResultsLoader {
  detail(id: string): Promise<IntelligenceRun>
  preview(id: string): Promise<IntelligencePreview>
  dispose(): void
}
export const intelligenceResultsKey: InjectionKey<IntelligenceResultsLoader> = Symbol('intelligence-results')

// 缓存局限当前页面，切换用户或离开页面即释放；所有卡片共用两个请求槽位。
export function createIntelligenceResultsLoader(admin = false): IntelligenceResultsLoader {
  const controller = new AbortController()
  const details = new Map<string, IntelligenceRun>()
  const previews = new Map<string, IntelligencePreview>()
  const pending = new Map<string, Promise<unknown>>()
  const queue: Array<() => void> = []
  let active = 0
  function schedule<T>(key: string, fn: () => Promise<T>): Promise<T> {
    if (pending.has(key)) return pending.get(key) as Promise<T>
    const work = new Promise<T>((resolve, reject) => {
      const execute = () => {
        if (controller.signal.aborted) { reject(new DOMException('Aborted', 'AbortError')); queue.shift()?.(); return }
        active++
        fn().then(resolve, reject).finally(() => { active--; queue.shift()?.() })
      }
      if (active < 2) execute()
      else queue.push(execute)
    }).finally(() => pending.delete(key))
    pending.set(key, work)
    return work
  }
  function remember<T>(cache: Map<string, T>, id: string, value: T): T {
    if (cache.size >= 80) cache.delete(cache.keys().next().value!)
    cache.set(id, value)
    return value
  }
  return {
    detail(id) {
      if (details.has(id)) return Promise.resolve(details.get(id)!)
      return schedule(`detail:${id}`, async () => {
        const result = await intelligenceAPI.detail(id, admin, controller.signal)
        // 运行中记录仍会变化，不能永久缓存为旧状态。
        return ['queued', 'submitting', 'running'].includes(result.status) ? result : remember(details, id, result)
      })
    },
    preview(id) {
      const cached = previews.get(id)
      if (cached && Date.parse(cached.expires_at) > Date.now() + 30000) return Promise.resolve(cached)
      return schedule(`preview:${id}`, async () => remember(previews, id, await intelligenceAPI.preview(id, admin, controller.signal)))
    },
    dispose() { controller.abort(); details.clear(); previews.clear(); while (queue.length) queue.shift()?.() },
  }
}

export function provideIntelligenceResults(admin = false): IntelligenceResultsLoader {
  const loader = createIntelligenceResultsLoader(admin)
  provide(intelligenceResultsKey, loader)
  onBeforeUnmount(loader.dispose)
  return loader
}

export function useIntelligenceResults(): IntelligenceResultsLoader {
  const loader = inject(intelligenceResultsKey)
  if (!loader) throw new Error('Intelligence results loader is missing')
  return loader
}
