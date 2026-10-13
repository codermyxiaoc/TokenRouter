import { apiClient } from './client'

// 活动资格、随机奖励和入账均由服务端计算，前端只传领取意图。
export type DailyActivityType = 'daily_gift' | 'recharge' | 'consumption'
export const dailyActivityTypes: DailyActivityType[] = [
  'daily_gift',
  'recharge',
  'consumption',
]
export interface DailyActivityRule {
  threshold: number
  reward_max: number
}
export interface DailyActivityConfig {
  enabled: boolean
  started_at: string | null
  daily_gift: DailyActivityRule
  recharge: DailyActivityRule
  consumption: DailyActivityRule
}
export interface DailyActivityStatus extends DailyActivityRule {
  type: DailyActivityType
  eligible_amount: number
  claimed_count: number
  available_count: number
  progress: number
}
export interface DailyActivitiesStatus {
  enabled: boolean
  started_at: string | null
  day: string
  activities: DailyActivityStatus[]
}
export interface DailyActivityClaim {
  id: number
  activity_type: DailyActivityType
  amount: number
  created_at: string
}
export interface DailyActivityClaimInput {
  activity_type: DailyActivityType
  all: boolean
  idempotency_key: string
}
export interface DailyActivityClaimResult {
  items: DailyActivityClaim[]
  amount: number
  balance: number
  remaining_count: number
  replayed: boolean
}
export interface DailyActivityHistory {
  items: DailyActivityClaim[]
  total: number
  page: number
  page_size: number
}
export const dailyActivitiesAPI = {
  async status(signal?: AbortSignal) {
    return (
      await apiClient.get<DailyActivitiesStatus>('/daily-activities', {
        signal,
      })
    ).data
  },
  async history(
    page = 1,
    activityType: DailyActivityType | '' = '',
    signal?: AbortSignal,
  ) {
    return (
      await apiClient.get<DailyActivityHistory>('/daily-activities/history', {
        params: {
          page,
          page_size: 20,
          ...(activityType ? { activity_type: activityType } : {}),
        },
        signal,
      })
    ).data
  },
  async claim(input: DailyActivityClaimInput) {
    return (
      await apiClient.post<DailyActivityClaimResult>(
        '/daily-activities/claim',
        input,
      )
    ).data
  },
  async config() {
    return (
      await apiClient.get<DailyActivityConfig>('/admin/settings/daily-activity')
    ).data
  },
  async saveConfig(config: Omit<DailyActivityConfig, 'started_at'>) {
    return (
      await apiClient.put<DailyActivityConfig>(
        '/admin/settings/daily-activity',
        config,
      )
    ).data
  },
}
