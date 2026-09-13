/**
 * 公告铃铛列表弹窗测试
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { nextTick } from 'vue'
import type { UserAnnouncement } from '@/types'
import { useAnnouncementStore } from '@/stores/announcements'
import AnnouncementBell from '../AnnouncementBell.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

vi.mock('@/utils/format', () => ({
  formatDateTime: () => '2026/08/01 12:00:00',
  formatRelativeTime: () => '3 hours ago',
}))

function createAnnouncement(
  id: number,
  overrides: Partial<UserAnnouncement> = {},
): UserAnnouncement {
  return {
    id,
    title: `Announcement ${id}`,
    content: `Content ${id}`,
    notify_mode: 'silent',
    created_at: `2026-08-01T${String(id).padStart(2, '0')}:00:00Z`,
    updated_at: `2026-08-01T${String(id).padStart(2, '0')}:00:00Z`,
    ...overrides,
  }
}

async function openAnnouncementList() {
  const wrapper = mount(AnnouncementBell, { attachTo: document.body })
  await wrapper.find('button').trigger('click')
  await nextTick()
  return wrapper
}

describe('AnnouncementBell', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  afterEach(() => {
    document.body.innerHTML = ''
    document.body.style.overflow = ''
    vi.restoreAllMocks()
  })

  it('使用与公告详情一致的轻量卡片并区分已读状态', async () => {
    const store = useAnnouncementStore()
    store.announcements = [
      createAnnouncement(1),
      createAnnouncement(2, { read_at: '2026-08-01T13:00:00Z' }),
    ]

    const wrapper = await openAnnouncementList()
    const dialog = document.body.querySelector('[data-testid="announcement-list-dialog"]')
    const items = document.body.querySelectorAll<HTMLButtonElement>(
      '[data-testid="announcement-list-item"]',
    )

    expect(dialog?.classList).toContain('max-w-[640px]')
    expect(dialog?.classList).toContain('rounded-surface')
    expect(dialog?.classList).toContain('sm:rounded-dialog')
    expect(dialog?.classList).toContain('dark:bg-dark-900')
    expect(dialog?.querySelector('[class*="bg-gradient"]')).toBeNull()
    expect(items).toHaveLength(2)
    expect(items[0]).toBeInstanceOf(HTMLButtonElement)
    expect(items[0].dataset.unread).toBe('true')
    expect(items[1].dataset.unread).toBe('false')
    expect(dialog?.querySelectorAll('[data-testid="announcement-list-status-unread"]')).toHaveLength(1)
    expect(dialog?.querySelectorAll('[data-testid="announcement-list-status-read"]')).toHaveLength(1)
    expect(dialog?.querySelectorAll('[data-testid="announcement-list-unread-pulse"]')).toHaveLength(1)
    expect(dialog?.textContent).toContain('1 announcements.unread')

    wrapper.unmount()
  })

  it('点击未读公告时标记已读，已读公告不会重复提交', async () => {
    const store = useAnnouncementStore()
    store.announcements = [
      createAnnouncement(1),
      createAnnouncement(2, { read_at: '2026-08-01T13:00:00Z' }),
    ]
    const markAsRead = vi.spyOn(store, 'markAsRead').mockResolvedValue()
    const wrapper = await openAnnouncementList()
    const items = document.body.querySelectorAll<HTMLButtonElement>(
      '[data-testid="announcement-list-item"]',
    )

    items[0].click()
    await flushPromises()
    items[1].click()
    await flushPromises()

    expect(markAsRead).toHaveBeenCalledTimes(1)
    expect(markAsRead).toHaveBeenCalledWith(1)

    wrapper.unmount()
  })

  it('以紧凑样式展示加载和空状态', async () => {
    const store = useAnnouncementStore()
    store.loading = true
    const wrapper = await openAnnouncementList()

    expect(document.body.querySelector('[data-testid="announcement-list-loading"]')).not.toBeNull()

    store.loading = false
    await nextTick()

    expect(document.body.querySelector('[data-testid="announcement-list-loading"]')).toBeNull()
    expect(document.body.textContent).toContain('announcements.empty')
    expect(document.body.textContent).toContain('announcements.emptyDescription')

    wrapper.unmount()
  })
})
