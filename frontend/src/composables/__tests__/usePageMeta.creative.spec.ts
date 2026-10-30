import { describe, expect, it, beforeEach, vi } from 'vitest'

// 只替换依赖：路由、i18n 与设置来源，验证创作台描述按公开设置的小时数计算天数。
let routeMeta: Record<string, unknown> = {}
let publicSettings: Record<string, unknown> | null = null
const params: Record<string, unknown>[] = []
const translations: Record<string, (p?: Record<string, unknown>) => string> = {
  'creative.description': (p) => `素材保留 ${p?.days} 天`,
  'creative.descriptionHours': (p) => `素材保留 ${p?.hours} 小时`,
  'keys.description': (p) => (p ? 'bad' : 'ok')
}

vi.mock('vue-router', () => ({ useRoute: () => ({ meta: routeMeta, name: 'Creative', params: {} }) }))
vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string, p?: Record<string, unknown>) => { params.push(p as Record<string, unknown>); return translations[key]?.(p) ?? key } })
}))
vi.mock('@/stores', () => ({
  useAppStore: () => ({ get cachedPublicSettings() { return publicSettings } }),
  useAuthStore: () => ({ isAdmin: false }),
  useAdminSettingsStore: () => ({ customMenuItems: [] })
}))

import { usePageMeta } from '../usePageMeta'

describe('创作台页面描述使用真实素材保存期', () => {
  beforeEach(() => { params.length = 0; routeMeta = { descriptionKey: 'creative.description' } })

  it('按公开设置的小时数换算天数，缺失或非法时回退 7 天', () => {
    publicSettings = { creative_canvas_asset_retention_hours: 48 }
    expect(usePageMeta().pageDescription.value).toBe('素材保留 2 天')
    publicSettings = { creative_canvas_asset_retention_hours: 168 }
    expect(usePageMeta().pageDescription.value).toBe('素材保留 7 天')
    publicSettings = {}
    expect(usePageMeta().pageDescription.value).toBe('素材保留 7 天')
    publicSettings = { creative_canvas_asset_retention_hours: -1 }
    expect(usePageMeta().pageDescription.value).toBe('素材保留 7 天')
    // 不足一天按小时描述，不能把 1/12/25 小时都说成 1 天。
    publicSettings = { creative_canvas_asset_retention_hours: 1 }
    expect(usePageMeta().pageDescription.value).toBe('素材保留 1 小时')
    publicSettings = { creative_canvas_asset_retention_hours: 12 }
    expect(usePageMeta().pageDescription.value).toBe('素材保留 12 小时')
    publicSettings = { creative_canvas_asset_retention_hours: 25 }
    expect(usePageMeta().pageDescription.value).toBe('素材保留 25 小时')
    publicSettings = { creative_canvas_asset_retention_hours: 48 }
    expect(usePageMeta().pageDescription.value).toBe('素材保留 2 天')
    publicSettings = null
    expect(usePageMeta().pageDescription.value).toBe('素材保留 7 天')
  })

  it('其它页面描述不传天数参数', () => {
    routeMeta = { descriptionKey: 'keys.description' }
    expect(usePageMeta().pageDescription.value).toBe('ok')
    expect(params.at(-1)).toBeUndefined()
  })
})
