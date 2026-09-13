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
})
