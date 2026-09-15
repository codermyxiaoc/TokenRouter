interface UsageBillingRow {
  billing_type?: number | null
  actual_cost?: number | null
  subscription_amount_usd?: number | null
  balance_amount_usd?: number | null
}

type UsageBillingType = 'balance' | 'subscription' | 'mixed' | 'none' | 'unknown'

// 按本次结算分配判断资金来源，不能用密钥当前配置推断历史扣费。
export function getUsageBillingType(row: UsageBillingRow): UsageBillingType {
  const hasSubscription = Number.isFinite(row.subscription_amount_usd) && (row.subscription_amount_usd ?? 0) > 0
  const hasBalance = Number.isFinite(row.balance_amount_usd) && (row.balance_amount_usd ?? 0) > 0
  if (hasSubscription && hasBalance) return 'mixed'
  if (hasSubscription) return 'subscription'
  if (hasBalance) return 'balance'
  // 零费用只说明本条记录未扣费，不据此推断免费或结算失败。
  if (row.actual_cost === 0) return 'none'
  // 历史记录可能没有分配金额，此时沿用记录自身的计费类型。
  if (row.billing_type === 0) return 'balance'
  if (row.billing_type === 1) return 'subscription'
  return 'unknown'
}

export function getUsageBillingTypeLabel(row: UsageBillingRow, t: (key: string) => string): string {
  switch (getUsageBillingType(row)) {
    case 'balance': return t('admin.usage.billingTypeBalance')
    case 'subscription': return t('admin.usage.billingTypeSubscription')
    case 'mixed': return t('admin.usage.billingTypeMixed')
    case 'none': return t('admin.usage.billingTypeNone')
    default: return t('admin.usage.billingTypeUnknown')
  }
}

export function getUsageBillingTypeBadgeClass(row: UsageBillingRow): string {
  switch (getUsageBillingType(row)) {
    case 'balance': return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
    case 'subscription': return 'bg-indigo-100 text-indigo-700 dark:bg-indigo-900/30 dark:text-indigo-300'
    case 'mixed': return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
    default: return 'bg-gray-100 text-gray-600 dark:bg-gray-800 dark:text-gray-400'
  }
}
