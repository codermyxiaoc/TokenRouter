/**
 * User Announcements API endpoints
 */

import { apiClient } from './client'
import type { UserAnnouncement } from '@/types'

export async function list(
  unreadOnly: boolean = false,
  bypassCache: boolean = false,
): Promise<UserAnnouncement[]> {
  const params: Record<string, number> = {}
  if (unreadOnly) params.unread_only = 1

  // 用户主动刷新时改变请求地址，避免浏览器复用个性化公告列表的旧响应。
  if (bypassCache) params.refresh_timestamp = Date.now()

  const { data } = await apiClient.get<UserAnnouncement[]>('/announcements', {
    params,
  })
  return data
}

export async function markRead(id: number): Promise<{ message: string }> {
  const { data } = await apiClient.post<{ message: string }>(`/announcements/${id}/read`)
  return data
}

const announcementsAPI = {
  list,
  markRead
}

export default announcementsAPI
