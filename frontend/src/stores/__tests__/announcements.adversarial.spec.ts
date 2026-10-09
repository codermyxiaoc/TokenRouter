import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useAnnouncementStore } from '../announcements'

const mocks = vi.hoisted(() => ({ list: vi.fn(), markRead: vi.fn() }))
vi.mock('@/api', () => ({ announcementsAPI: mocks }))
beforeEach(() => { setActivePinia(createPinia()); vi.resetAllMocks() })
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks() })
function pending<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: Error) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
const announcement = (id: number) => ({ id, title: `公告 ${id}`, content: '测试', notify_mode: 'silent', read_at: null })

describe('公告跨用户异步边界', () => {
  it.each(['success', 'failure'])('旧用户列表 %s 不回填新会话', async (outcome) => {
    const store = useAnnouncementStore()
    const old = pending<unknown[]>()
    mocks.list.mockReturnValueOnce(old.promise).mockResolvedValueOnce([announcement(2)])
    const previous = store.fetchAnnouncements()
    store.reset()
    await store.fetchAnnouncements()
    if (outcome === 'success') old.resolve([announcement(1)])
    else old.reject(new Error('旧会话错误'))
    await previous
    expect(store.announcements.map(item => item.id)).toEqual([2])
    expect(store.loading).toBe(false)
  })

  it('旧用户单条标读完成后不修改新用户同 ID 公告', async () => {
    const store = useAnnouncementStore()
    const old = pending<void>()
    mocks.list.mockResolvedValue([announcement(1)])
    mocks.markRead.mockReturnValue(old.promise)
    await store.fetchAnnouncements()
    const previous = store.markAsRead(1)
    store.reset()
    await store.fetchAnnouncements()
    old.resolve()
    await previous
    expect(store.announcements[0]?.read_at).toBeNull()
  })

  it('旧用户全部标读完成后不修改新用户不同 ID 公告', async () => {
    const store = useAnnouncementStore()
    const old = pending<void>()
    mocks.list.mockResolvedValueOnce([announcement(1)]).mockResolvedValueOnce([announcement(2)])
    mocks.markRead.mockReturnValue(old.promise)
    await store.fetchAnnouncements()
    const previous = store.markAllAsRead()
    store.reset()
    await store.fetchAnnouncements()
    old.resolve()
    await previous
    expect(store.announcements[0]?.read_at).toBeNull()
  })

  it.each([
    ['single', 'success', 1], ['single', 'failure', 1], ['single', 'failure', 2],
    ['all', 'success', 1], ['all', 'failure', 1], ['all', 'failure', 2],
  ] as const)('旧会话 %s 标读 %s 后不影响新会话 ID=%s 的请求或错误状态', async (operation, outcome, nextId) => {
    const store = useAnnouncementStore()
    const old = pending<void>()
    const current = pending<unknown[]>()
    const logError = vi.spyOn(console, 'error').mockImplementation(() => {})
    mocks.list.mockResolvedValueOnce([announcement(1)]).mockReturnValueOnce(current.promise)
    mocks.markRead.mockReturnValueOnce(old.promise)
    await store.fetchAnnouncements()
    const previous = operation === 'single' ? store.markAsRead(1) : store.markAllAsRead()
    store.reset()
    const next = store.fetchAnnouncements()
    if (outcome === 'success') old.resolve()
    else old.reject(new Error('旧会话标读失败'))
    expect(await previous).toBeUndefined()
    expect(store.loading).toBe(true)
    expect(logError).not.toHaveBeenCalled()
    current.resolve([announcement(nextId)])
    await next
    expect(store.announcements[0]?.read_at).toBeNull()
    expect(store.loading).toBe(false)
  })

  it.each(['single', 'all'])('同一会话 %s 标读响应仍有效，刷新新增的公告保持未读', async (operation) => {
    const store = useAnnouncementStore()
    const old = pending<void>()
    mocks.list.mockResolvedValueOnce([announcement(1)]).mockResolvedValueOnce([announcement(1), announcement(2)])
    mocks.markRead.mockReturnValueOnce(old.promise)
    await store.fetchAnnouncements()
    const request = operation === 'single' ? store.markAsRead(1) : store.markAllAsRead()
    await store.fetchAnnouncements(true)
    old.resolve()
    expect(await request).toBe(true)
    expect(store.announcements[0]?.read_at).toEqual(expect.any(String))
    expect(store.announcements[1]?.read_at).toBeNull()
  })

  it.each(['single', 'all'])('当前会话 %s 标读失败保持未读并返回失败', async (operation) => {
    const store = useAnnouncementStore()
    const error = new Error('当前会话标读失败')
    vi.spyOn(console, 'error').mockImplementation(() => {})
    mocks.list.mockResolvedValueOnce([announcement(1)])
    mocks.markRead.mockRejectedValueOnce(error)
    await store.fetchAnnouncements()
    if (operation === 'single') expect(await store.markAsRead(1)).toBe(false)
    else await expect(store.markAllAsRead()).rejects.toBe(error)
    expect(store.announcements[0]?.read_at).toBeNull()
    expect(store.loading).toBe(false)
  })

  it('旧会话延迟弹窗不会跳过新会话当前公告', async () => {
    vi.useFakeTimers()
    const store = useAnnouncementStore()
    mocks.list.mockResolvedValueOnce([1, 2].map(id => ({ ...announcement(id), notify_mode: 'popup' })))
      .mockResolvedValueOnce([3, 4].map(id => ({ ...announcement(id), notify_mode: 'popup' })))
    await store.fetchAnnouncements()
    await store.dismissPopup()
    store.reset()
    await store.fetchAnnouncements()
    expect(store.currentPopup?.id).toBe(3)
    await vi.advanceTimersByTimeAsync(300)
    expect(store.currentPopup?.id).toBe(3)
  })
})
