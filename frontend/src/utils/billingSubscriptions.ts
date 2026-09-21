import type { BillingSubscription } from '@/types'

interface BillingSubscriptionRow {
  billing_subscriptions?: BillingSubscription[] | null
}

// 后端摘要已按实际结算归属聚合；前端不从订阅对象或密钥配置猜测套餐。
export function getBillingSubscriptions(row: BillingSubscriptionRow): BillingSubscription[] {
  return (row.billing_subscriptions ?? []).filter((item) =>
    Number.isSafeInteger(item.subscription_id) && item.subscription_id > 0
    && Number.isFinite(item.amount_usd) && item.amount_usd > 0,
  )
}

export function getBillingSubscriptionName(item: BillingSubscription, subscriptionLabel: string): string {
  return item.plan_name?.trim() || `${subscriptionLabel} #${item.subscription_id}`
}

// 导出保留订阅实例 ID，方便区分同名套餐的多次购买。
export function getBillingSubscriptionsExport(row: BillingSubscriptionRow, subscriptionLabel: string): string {
  return getBillingSubscriptions(row).map((item) => {
    const name = getBillingSubscriptionName(item, subscriptionLabel)
    return item.plan_name?.trim() ? `${name} (#${item.subscription_id})` : name
  }).join('; ')
}
