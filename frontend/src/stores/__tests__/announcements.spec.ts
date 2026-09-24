/**
 * 公告 store 强制刷新测试
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useAnnouncementStore } from '@/stores/announcements'

const mockList = vi.fn()

vi.mock('@/api', () => ({
  announcementsAPI: {
    list: (...args: unknown[]) => mockList(...args),
    markRead: vi.fn(),
  },
}))

describe('useAnnouncementStore', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
    mockList.mockResolvedValue([])
  })

  it('强制刷新时同时绕过请求节流和浏览器缓存', async () => {
    const store = useAnnouncementStore()

    await store.fetchAnnouncements()
    await store.fetchAnnouncements(true)

    expect(mockList).toHaveBeenNthCalledWith(1, false, false)
    expect(mockList).toHaveBeenNthCalledWith(2, false, true)
  })

  it('保留完整可见公告列表供不同界面独立排序', async () => {
    const store = useAnnouncementStore()
    mockList.mockResolvedValue(Array.from({ length: 25 }, (_, index) => ({
      id: index + 1,
      title: `公告 ${index + 1}`,
      content: `内容 ${index + 1}`,
      notify_mode: 'silent',
      created_at: '2026-08-01T12:00:00Z',
      updated_at: '2026-08-01T12:00:00Z',
    })))

    await store.fetchAnnouncements()

    expect(store.announcements).toHaveLength(25)
  })

  it('重置会话后迟到的旧公告不会覆盖新会话或结束新请求的加载状态', async () => {
    const store = useAnnouncementStore()
    let resolveOld!: (value: unknown[]) => void
    let resolveNew!: (value: unknown[]) => void
    mockList.mockReturnValueOnce(new Promise(resolve => { resolveOld = resolve }))
    const oldRequest = store.fetchAnnouncements()
    store.reset()
    mockList.mockReturnValueOnce(new Promise(resolve => { resolveNew = resolve }))
    const newRequest = store.fetchAnnouncements()
    resolveOld([{ id: 1, notify_mode: 'popup' }])
    await oldRequest
    expect(store.announcements).toEqual([])
    expect(store.currentPopup).toBeNull()
    expect(store.loading).toBe(true)
    resolveNew([{ id: 2, notify_mode: 'silent' }])
    await newRequest
    expect(store.announcements.map(item => item.id)).toEqual([2])
    expect(store.loading).toBe(false)
  })
})
