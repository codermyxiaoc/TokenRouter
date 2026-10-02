import type { MediaTask } from '@/api/mediaTasks'

// 视频金额仅在展示时保留五位小数，不能把舍入值回写账本或用于结算。
export function formatVideoTaskAmount(value: string | number | null | undefined): string | null {
  if (value == null || String(value).trim() === '') return null
  const amount = Number(value)
  if (!Number.isFinite(amount)) return null
  const formatted = amount.toFixed(5)
  // 非零小额费用舍入到零时仍表明已发生消费，区分真正的免费任务。
  return amount > 0 && formatted === '0.00000' ? '<$0.00001' : `$${formatted}`
}

type MediaTaskBillingState = Pick<MediaTask, 'source' | 'video_billing'>

// 待核对是资金状态，不覆盖已确定的生成终态，也不改变后端筛选语义。
export function isVideoTaskReconciliation(task: MediaTaskBillingState): boolean {
  return task.source === 'video' && task.video_billing?.status === 'reconciliation'
}

export function getMediaTaskDisplayStatus(task: MediaTaskBillingState & Pick<MediaTask, 'status'>): string {
  return isVideoTaskReconciliation(task) && ['queued', 'processing'].includes(task.status)
    ? 'reconciliation' : task.status
}

export function formatMediaTaskCost(
  task: Pick<MediaTask, 'source' | 'actual_cost' | 'video_billing'>,
  pendingLabel: string,
  billingLabels: { released: string; reconciliation: string },
): string {
  if (task.source === 'video') {
    // 释放由服务端账务确认；历史预留金额或缺少用量记录都不能再显示为待扣费。
    if (task.video_billing?.status === 'released') return billingLabels.released
    if (isVideoTaskReconciliation(task)) return billingLabels.reconciliation
    return formatVideoTaskAmount(task.actual_cost) ?? pendingLabel
  }
  // 其它来源沿用原有展示精度；缺少账本金额不能根据完成状态推断免费。
  const value = task.actual_cost
  if (value === null || value === undefined || value === '') return pendingLabel
  const amount = Number(value)
  return Number.isFinite(amount) ? `$${amount.toFixed(10).replace(/0+$/, '').replace(/\.$/, '.00')}` : pendingLabel
}
