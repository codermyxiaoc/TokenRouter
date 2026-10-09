import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { announcementsAPI } from '@/api'
import type { UserAnnouncement } from '@/types'

const THROTTLE_MS = 20 * 60 * 1000 // 20 minutes
const MAX_POPUP_CANDIDATES = 20

export const useAnnouncementStore = defineStore('announcements', () => {
  // State
  const announcements = ref<UserAnnouncement[]>([])
  const loading = ref(false)
  const lastFetchTime = ref(0)
  const popupQueue = ref<UserAnnouncement[]>([])
  const currentPopup = ref<UserAnnouncement | null>(null)

  // Session-scoped dedup set — not reactive, used as plain lookup only
  let shownPopupIds = new Set<number>()
  // 会话重置或更新请求后，旧请求不得回填其他会话的公告。
  let fetchGeneration = 0
  // 列表刷新与会话切换分别计数，正常刷新不能让同会话的标读请求失效。
  let sessionGeneration = 0

  // Getters
  const unreadCount = computed(() =>
    announcements.value.filter((a) => !a.read_at).length
  )

  // Actions
  async function fetchAnnouncements(force = false) {
    const now = Date.now()
    if (!force && lastFetchTime.value > 0 && now - lastFetchTime.value < THROTTLE_MS) {
      return
    }

    // Set immediately to prevent concurrent duplicate requests
    lastFetchTime.value = now
    const generation = ++fetchGeneration

    try {
      loading.value = true
      const all = await announcementsAPI.list(false, force)
      if (generation !== fetchGeneration) return
      // 保留完整可见列表，确保不同界面可以按各自规则排序和统计。
      announcements.value = all
      enqueueNewPopups()
    } catch (err: any) {
      if (generation !== fetchGeneration) return
      // Revert throttle timestamp on failure so retry is allowed
      lastFetchTime.value = 0
      console.error('Failed to fetch announcements:', err)
    } finally {
      if (generation === fetchGeneration) loading.value = false
    }
  }

  function enqueueNewPopups() {
    // 弹窗候选仍限制为服务端优先级最高的前 20 条，避免一次会话堆积过多弹窗。
    const newPopups = announcements.value
      .slice(0, MAX_POPUP_CANDIDATES)
      .filter((a) => a.notify_mode === 'popup' && !a.read_at && !shownPopupIds.has(a.id))
    if (newPopups.length === 0) return

    for (const p of newPopups) {
      if (!popupQueue.value.some((q) => q.id === p.id)) {
        popupQueue.value.push(p)
      }
    }

    if (!currentPopup.value) {
      showNextPopup()
    }
  }

  function showNextPopup() {
    if (popupQueue.value.length === 0) {
      currentPopup.value = null
      return
    }
    currentPopup.value = popupQueue.value.shift()!
    shownPopupIds.add(currentPopup.value.id)
  }

  async function dismissPopup() {
    if (!currentPopup.value) return
    const generation = sessionGeneration
    const id = currentPopup.value.id
    currentPopup.value = null

    // Mark as read (fire-and-forget, UI already updated)
    markAsRead(id)

    // Show next popup after a short delay
    if (popupQueue.value.length > 0) {
      setTimeout(() => {
        if (generation === sessionGeneration) showNextPopup()
      }, 300)
    }
  }

  async function markAsRead(id: number) {
    const generation = sessionGeneration
    try {
      await announcementsAPI.markRead(id)
      if (generation !== sessionGeneration) return
      const ann = announcements.value.find((a) => a.id === id)
      if (ann) {
        ann.read_at = new Date().toISOString()
      }
      return true
    } catch (err: any) {
      // 旧会话的失败也静默丢弃，不能向新用户弹出标读错误。
      if (generation !== sessionGeneration) return
      console.error('Failed to mark announcement as read:', err)
      return false
    }
  }

  async function markAllAsRead() {
    const generation = sessionGeneration
    const unread = announcements.value.filter((a) => !a.read_at)
    if (unread.length === 0) return true
    const unreadIds = new Set(unread.map((a) => a.id))

    try {
      loading.value = true
      await Promise.all(unread.map((a) => announcementsAPI.markRead(a.id)))
      if (generation !== sessionGeneration) return
      announcements.value.forEach((a) => {
        // 同会话刷新后新出现的公告并未发起标读，仍须保持未读。
        if (unreadIds.has(a.id) && !a.read_at) {
          a.read_at = new Date().toISOString()
        }
      })
      return true
    } catch (err: any) {
      if (generation !== sessionGeneration) return
      console.error('Failed to mark all as read:', err)
      throw err
    } finally {
      if (generation === sessionGeneration) loading.value = false
    }
  }

  function reset() {
    sessionGeneration++
    fetchGeneration++
    announcements.value = []
    lastFetchTime.value = 0
    shownPopupIds = new Set()
    popupQueue.value = []
    currentPopup.value = null
    loading.value = false
  }

  return {
    // State
    announcements,
    loading,
    currentPopup,
    // Getters
    unreadCount,
    // Actions
    fetchAnnouncements,
    dismissPopup,
    markAsRead,
    markAllAsRead,
    reset,
  }
})
