import type { SubscriptionPlan } from '@/types/payment'

type TranslateFn = (key: string) => string

/**
 * 生成用户侧套餐有效期后缀，例如“月”、“30天”或“2周”。
 *
 * 管理端和历史数据可能分别使用单数或复数单位。后端仅把 week/weeks 与
 * month/months 换算为天，其余单位按天处理；这里保持相同语义，避免展示
 * 周期与实际生效周期不一致。
 */
export function planValiditySuffix(
  plan: Pick<SubscriptionPlan, 'validity_days' | 'validity_unit'>,
  t: TranslateFn,
): string {
  const unit = String(plan.validity_unit || 'day').trim().toLowerCase()
  const base = unit.endsWith('s') ? unit.slice(0, -1) : unit
  const days = plan.validity_days
  if (base === 'month') {
    return days === 1 ? t('payment.perMonth') : `${days}${t('payment.months')}`
  }
  if (base === 'week') {
    return `${days}${t('payment.weeks')}`
  }
  // 其余单位与后端一致按天解释。
  return `${days}${t('payment.days')}`
}
