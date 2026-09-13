/**
 * 用户仪表盘公告时间线测试
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { nextTick } from 'vue'
import type { UserAnnouncement } from '@/types'
import { useAnnouncementStore } from '@/stores/announcements'
import UserDashboardAnnouncements from '../UserDashboardAnnouncements.vue'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => {
        if (key === 'dashboard.unreadAnnouncements') {
          return `${params?.count ?? 0} unread`
        }
        return key
      },
    }),
  }
})

vi.mock('@/utils/format', () => ({
  formatDate: (date: string | Date, options: Intl.DateTimeFormatOptions) =>
    new Intl.DateTimeFormat('zh-CN', options).format(new Date(date)),
  formatDateTime: () => '2026/08/01 12:00:00',
  formatRelativeTime: () => '2 hours ago',
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

describe('UserDashboardAnnouncements', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  afterEach(() => {
    document.body.innerHTML = ''
    document.body.style.overflow = ''
    vi.restoreAllMocks()
  })

  it('按发布时间倒序展示最近五条并生成安全纯文本摘要', () => {
    const store = useAnnouncementStore()
    store.announcements = [2, 6, 1, 5, 3, 4].map((id) =>
      createAnnouncement(id, {
        read_at: id % 2 === 0 ? undefined : '2026-08-01T13:00:00Z',
        content: id === 6
          ? '# Release **ready**\n\n<script>unsafe()</script><p>More details</p>'
          : `Content ${id}`,
      }),
    )

    const wrapper = mount(UserDashboardAnnouncements)
    const items = wrapper.findAll('[data-testid="announcement-timeline-item"]')
    const nodes = wrapper.findAll('[data-testid="announcement-timeline-node"]')
    const connectors = wrapper.findAll('[data-testid="announcement-timeline-connector"]')
    const pulses = wrapper.findAll('[data-testid="announcement-timeline-pulse"]')

    expect(items).toHaveLength(5)
    expect(items.map((item) => item.attributes('data-announcement-id'))).toEqual([
      '6',
      '5',
      '4',
      '3',
      '2',
    ])
    expect(connectors).toHaveLength(4)
    expect(connectors[0].classes()).toContain('top-[1.375rem]')
    expect(connectors[0].classes()).toContain('bottom-[-1.625rem]')
    expect(wrapper.find('[data-testid="announcement-unread-count"]').text()).toBe('3 unread')
    expect(nodes[0].attributes('data-unread')).toBe('true')
    expect(nodes[0].classes()).toContain('bg-primary-500')
    expect(nodes[0].classes()).toContain('top-4')
    expect(nodes[1].attributes('data-unread')).toBe('false')
    expect(nodes[1].classes()).toContain('bg-gray-400')
    expect(pulses).toHaveLength(3)
    expect(pulses[0].classes()).toContain('animate-ping')
    expect(pulses[0].classes()).toContain('motion-reduce:animate-none')
    expect(items[0].text()).toContain('Release ready More details')
    expect(items[0].text()).not.toContain('unsafe()')
    expect(wrapper.html()).not.toContain('<script')

    wrapper.unmount()
  })

  it('同年日期省略年份，跨年日期显示年份', () => {
    const store = useAnnouncementStore()
    const currentYear = new Date().getFullYear()
    store.announcements = [
      createAnnouncement(1, { created_at: `${currentYear - 1}-08-01T12:00:00` }),
      createAnnouncement(2, { created_at: `${currentYear}-08-01T12:00:00` }),
    ]

    const wrapper = mount(UserDashboardAnnouncements)
    const dates = wrapper.findAll('time').map((time) => time.text())

    expect(dates).toEqual(['8月1日', `${currentYear - 1}年8月1日`])

    wrapper.unmount()
  })

  it('分别展示加载状态和空状态', async () => {
    const store = useAnnouncementStore()
    store.loading = true
    const wrapper = mount(UserDashboardAnnouncements)

    expect(wrapper.find('[data-testid="announcement-timeline-loading"]').exists()).toBe(true)

    store.loading = false
    await nextTick()

    expect(wrapper.find('[data-testid="announcement-timeline-loading"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="announcement-unread-count"]').text()).toBe('0 unread')
    expect(wrapper.text()).toContain('announcements.empty')
    expect(wrapper.text()).toContain('announcements.emptyDescription')

    wrapper.unmount()
  })

  it('点击未读公告打开详情并标记已读，已读公告不重复标记', async () => {
    const store = useAnnouncementStore()
    store.announcements = [
      createAnnouncement(1, { title: 'Unread announcement' }),
      createAnnouncement(2, {
        title: 'Read announcement',
        read_at: '2026-08-01T13:00:00Z',
      }),
    ]
    const markAsRead = vi.spyOn(store, 'markAsRead').mockResolvedValue()
    const wrapper = mount(UserDashboardAnnouncements, { attachTo: document.body })
    const unreadItem = wrapper.find('[data-announcement-id="1"]')
    const readItem = wrapper.find('[data-announcement-id="2"]')

    await unreadItem.trigger('click')
    await flushPromises()

    expect(markAsRead).toHaveBeenCalledTimes(1)
    expect(markAsRead).toHaveBeenCalledWith(1)
    expect(document.body.textContent).toContain('Unread announcement')
    expect(document.body.querySelector('[data-testid="announcement-popup-dismiss"]')).not.toBeNull()

    document.body
      .querySelector<HTMLButtonElement>('[data-testid="announcement-popup-dismiss"]')
      ?.click()
    await nextTick()
    await readItem.trigger('click')
    await flushPromises()

    expect(markAsRead).toHaveBeenCalledTimes(1)
    expect(document.body.textContent).toContain('Read announcement')

    wrapper.unmount()
  })
})
