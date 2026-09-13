/**
 * User Subscription API
 * API for regular users to view their own subscriptions and progress
 */

import { apiClient } from './client'
import type {
  UserSubscription,
  SubscriptionProgress,
  SubscriptionProgressInfo
} from '@/types'

// 撤销订阅接口返回被撤销记录、接续记录和改绑 Key 数量。
export interface RevokeSubscriptionResponse {
  revoked_subscription_id: number
  replacement_subscription_id: number | null
  rebound_api_key_count: number
}

/**
 * Subscription summary for user dashboard
 */
export interface SubscriptionSummary {
  active_count: number
  total_used_usd?: number
  subscriptions: Array<{
    id: number
    plan_id: number
    plan_name: string
    status: string
    daily_used_usd: number
    daily_limit_usd: number
    weekly_used_usd: number
    weekly_limit_usd: number
    monthly_used_usd: number
    monthly_limit_usd: number
    expires_at: string | null
  }>
}

/**
 * Get list of current user's subscriptions
 */
export async function getMySubscriptions(): Promise<UserSubscription[]> {
  const response = await apiClient.get<UserSubscription[]>('/subscriptions')
  return response.data
}

/**
 * Get current user's active subscriptions
 */
export async function getActiveSubscriptions(): Promise<UserSubscription[]> {
  const response = await apiClient.get<UserSubscription[]>('/subscriptions/active')
  return response.data
}

/**
 * Get progress for all user's active subscriptions
 */
export async function getSubscriptionsProgress(): Promise<SubscriptionProgressInfo[]> {
  const response = await apiClient.get<SubscriptionProgressInfo[]>('/subscriptions/progress')
  return response.data
}

/**
 * Get subscription summary for dashboard display
 */
export async function getSubscriptionSummary(): Promise<SubscriptionSummary> {
  const response = await apiClient.get<SubscriptionSummary>('/subscriptions/summary')
  return response.data
}

/**
 * Get progress for a specific subscription
 */
export async function getSubscriptionProgress(
  subscriptionId: number
): Promise<SubscriptionProgress> {
  const response = await apiClient.get<SubscriptionProgress>(
    `/subscriptions/${subscriptionId}/progress`
  )
  return response.data
}

/** 撤销当前用户额度耗尽的订阅，并在有接续包时改绑显式订阅 Key。 */
export async function revokeExhaustedSubscription(
  subscriptionId: number
): Promise<RevokeSubscriptionResponse> {
  const response = await apiClient.post<RevokeSubscriptionResponse>(
    `/subscriptions/${subscriptionId}/revoke`
  )
  return response.data
}

export default {
  getMySubscriptions,
  getActiveSubscriptions,
  getSubscriptionsProgress,
  getSubscriptionSummary,
  getSubscriptionProgress,
  revokeExhaustedSubscription
}
